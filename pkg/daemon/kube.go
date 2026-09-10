/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package daemon

import (
	"bytes"
	"context"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	providerregistry "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/kube/controller"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"go.yaml.in/yaml/v3"
)

// Backoff bounds for restarting a controller that could not start: the usual cause is a briefly
// unreachable API server, and the ceiling keeps a bad connection from hammering it
const (
	defaultKubeRetryMin = time.Second
	defaultKubeRetryMax = 30 * time.Second
)

// kubeRunner is the controller as the daemon uses it
type kubeRunner interface {
	Start(context.Context) error
	Resync()
	Stop()
}

// newKubeRunner builds a controller; indirected so tests can substitute one
// that needs no cluster
var newKubeRunner = func(cfg controller.Config) (kubeRunner, error) {
	return controller.New(cfg)
}

// kubeSupervisor owns the Kubernetes controller's lifecycle and is the daemon's overlay
// provider; every transition is asynchronous, since the controller and reload call each other
type kubeSupervisor struct {
	ctx      context.Context
	reloader reload.Reloader
	certs    controller.CertSink
	// readiness is held pending while no generated configuration is serving and told once a
	// controller's translation is applied, so a pod is not routed to before its routes exist
	readiness *ready.State
	// overlay is the last overlay published, read by every reload
	overlay atomic.Pointer[config.Overlay]

	// certState is the certificate inventory every generation shares, so a controller built
	// from a new configuration can withdraw what its predecessor installed
	certState *controller.CertState
	// known is what the running configuration defines that a route may name, republished on
	// every applied configuration because a route may name one at any time
	known atomic.Pointer[ir.ConfiguredNames]
	// tracer is the tracer the controller's own spans report to, the one
	// defaults.tracing_name names; republished on every applied configuration
	tracer atomic.Pointer[tracing.Tracer]

	// publishMtx serializes everything that changes the published overlay, so a generation
	// check and the store that follows it cannot be interleaved by another generation
	publishMtx sync.Mutex
	// swapMtx serializes retirement and startup across generations, so no transition starts a
	// controller alongside one still finishing a pass; two would undo each other's certificates
	swapMtx sync.Mutex

	mtx sync.Mutex
	// applied is the serialized section the running controller was built
	// from; an identical section is not a restart
	applied []byte
	current kubeRunner
	// generation invalidates a start that is still in flight when a newer
	// configuration arrives
	generation uint64
	closed     bool
	wg         sync.WaitGroup

	// retryMin and retryMax bound the restart backoff; they are set once at
	// construction so no transition in flight reads them changing
	retryMin, retryMax time.Duration
}

func newKubeSupervisor(ctx context.Context, si *instance.ServerInstance,
	reloader reload.Reloader,
) *kubeSupervisor {
	s := &kubeSupervisor{
		ctx: ctx, reloader: reloader, certState: controller.NewCertState(),
		retryMin: defaultKubeRetryMin, retryMax: defaultKubeRetryMax,
	}
	if si != nil {
		if si.CertMonitor != nil {
			s.certs = si.CertMonitor
		}
		s.readiness = si.Readiness
	}
	return s
}

// Overlay returns the controller's current configuration overlay
func (s *kubeSupervisor) Overlay() *config.Overlay {
	if s == nil {
		return nil
	}
	return s.overlay.Load()
}

// fencedPublisher is one controller generation's way of reaching the daemon; the generation
// keeps a retired controller from restoring routes of a scope no longer configured
type fencedPublisher struct {
	supervisor *kubeSupervisor
	generation uint64
}

func (p fencedPublisher) Publish(o *config.Overlay) (bool, error) {
	return p.supervisor.publish(p.generation, o)
}

func (s *kubeSupervisor) publish(generation uint64, o *config.Overlay) (bool, error) {
	s.publishMtx.Lock()
	defer s.publishMtx.Unlock()
	if !s.stillWanted(generation) {
		return false, controller.ErrRetired
	}
	previous := s.overlay.Load()
	s.overlay.Store(o)
	changed, err := s.reloader(reload.SourceKubernetes)
	if err != nil {
		s.overlay.Store(previous)
		return changed, err
	}
	// the generated configuration is serving, whether this reload or an identical earlier one
	// put it there; a translation that failed before reaching here leaves readiness as it was
	s.readiness.SetProgrammed()
	return changed, nil
}

func (s *kubeSupervisor) unload(generation uint64) {
	s.publishMtx.Lock()
	defer s.publishMtx.Unlock()
	if !s.stillWanted(generation) {
		return
	}
	// the controller is gone, and so is everything it generated; the reload
	// is what actually takes those routes out of service
	s.overlay.Store(nil)
	if _, err := s.reloader(reload.SourceKubernetes); err != nil {
		logger.Error("could not unload kubernetes controller configuration",
			logging.Pairs{keys.Error: err.Error()})
	}
}

func (s *kubeSupervisor) knownNames() ir.ConfiguredNames {
	if n := s.known.Load(); n != nil {
		return *n
	}
	return ir.ConfiguredNames{}
}

// Apply reconciles the running controller with a newly applied configuration: an unchanged
// kubernetes section is a no-op, and any change restarts the controller
func (s *kubeSupervisor) Apply(conf *config.Config, tracers tracing.Tracers) {
	var opts *kubecfg.Options
	if conf != nil && conf.Kubernetes.IsEnabled() {
		opts = conf.Kubernetes.Clone()
	}
	s.setTracer(opts, tracers)
	// the name sets are republished on every applied configuration: a route may name a cache
	// the operator adds later, and the running controller has to stop rejecting it
	changedNames := s.setKnownNames(conf)
	want := marshalKubeOptions(opts)
	s.mtx.Lock()
	if s.closed || bytes.Equal(want, s.applied) {
		current := s.current
		s.mtx.Unlock()
		if changedNames && current != nil {
			current.Resync()
		}
		return
	}
	s.applied = want
	s.generation++
	generation := s.generation
	s.wg.Add(1)
	s.mtx.Unlock()
	if opts != nil && s.overlay.Load() == nil {
		// a controller is being installed with nothing of a predecessor's serving, so the pod
		// is not ready until this one has published; a replacement keeps serving meanwhile
		s.readiness.SetPending()
	}
	safego.Go(reloadGoroutinePanic("kubeSupervisor", reload.SourceKubernetes),
		func() {
			defer s.wg.Done()
			s.swap(generation, opts)
		})
}

// Close stops the controller and waits for any transition in flight
func (s *kubeSupervisor) Close() {
	s.mtx.Lock()
	if s.closed {
		s.mtx.Unlock()
		s.wg.Wait()
		return
	}
	s.closed = true
	current := s.current
	s.current = nil
	s.mtx.Unlock()
	if current != nil {
		// deliberately not under the transition lock: a transition can be blocked in a
		// controller's Start on an unreachable cluster, and stopping it is what unblocks it
		current.Stop()
	}
	s.wg.Wait()
}

func (s *kubeSupervisor) swap(generation uint64, opts *kubecfg.Options) {
	s.swapMtx.Lock()
	wanted := s.stillWanted(generation)
	if wanted {
		s.retire()
		wanted = s.stillWanted(generation)
	}
	s.swapMtx.Unlock()
	if !wanted {
		// a newer configuration owns the controller, since before this transition took the lock
		// or since it began retiring, and it starts the controller this one would have
		return
	}
	if opts == nil {
		s.unload(generation)
		// with no controller there is no first translation to wait for
		s.readiness.SetProgrammed()
		return
	}
	delay := s.retryMin
	for {
		if !s.start(generation, opts) {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > s.retryMax {
			delay = s.retryMax
		}
	}
}

func (s *kubeSupervisor) retire() {
	s.mtx.Lock()
	current := s.current
	s.current = nil
	s.mtx.Unlock()
	if current != nil {
		current.Stop()
	}
}

func (s *kubeSupervisor) start(generation uint64, opts *kubecfg.Options) bool {
	s.swapMtx.Lock()
	c, err := s.build(generation, opts)
	if err != nil {
		s.swapMtx.Unlock()
		logger.Error("could not start the kubernetes controller",
			logging.Pairs{keys.Error: err.Error()})
		return s.stillWanted(generation)
	}
	if c == nil {
		s.swapMtx.Unlock()
		return false
	}
	adopted := s.adopt(generation, c)
	s.swapMtx.Unlock()
	if !adopted {
		c.Stop()
		return false
	}
	if err = c.Start(s.ctx); err != nil {
		logger.Error("kubernetes controller did not start",
			logging.Pairs{keys.Error: err.Error()})
		c.Stop()
		// a controller that failed to start holds nothing; dropping it here
		// keeps Close and the next Apply from stopping it a second time
		s.disown(generation, c)
		return s.stillWanted(generation)
	}
	logger.Info("kubernetes controller started", nil)
	return false
}

func (s *kubeSupervisor) build(generation uint64, opts *kubecfg.Options,
) (kubeRunner, error) {
	if !s.stillWanted(generation) {
		// a newer configuration overtook this one while it was waiting to
		// retry; building a controller only to discard it helps nobody
		return nil, nil
	}
	return newKubeRunner(controller.Config{
		Options:       opts,
		Publisher:     fencedPublisher{supervisor: s, generation: generation},
		Certs:         s.certs,
		CertState:     s.certState,
		KnownNames:    s.knownNames,
		Tracer:        s.tracer.Load,
		ProviderPaths: providerPaths,
	})
}

// providerPathsOnce reads each time series provider's predefined paths once, from the provider's
// own client, so a route served through the provider cannot declare one of them itself
var providerPathsOnce = sync.OnceValue(func() map[string]po.List {
	return readProviderPaths(providerregistry.SupportedProviders())
})

func readProviderPaths(factories rt.Lookup) map[string]po.List {
	out := make(map[string]po.List)
	for name, factory := range factories {
		if !providers.IsSupportedHTTPTimeSeriesProvider(name) {
			continue
		}
		o := bo.New()
		o.Provider = name
		client, err := factory(name, o, nil, nil, nil, factories)
		if err != nil {
			continue
		}
		out[name] = client.DefaultPathConfigs(o)
	}
	return out
}

func providerPaths(provider string) po.List {
	return providerPathsOnce()[provider]
}

func (s *kubeSupervisor) setTracer(opts *kubecfg.Options, tracers tracing.Tracers) {
	if opts == nil || opts.Defaults == nil || opts.Defaults.TracingName == "" {
		s.tracer.Store(nil)
		return
	}
	s.tracer.Store(tracers[opts.Defaults.TracingName])
}

func (s *kubeSupervisor) adopt(generation uint64, c kubeRunner) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.closed || s.generation != generation {
		return false
	}
	s.current = c
	return true
}

func (s *kubeSupervisor) disown(generation uint64, c kubeRunner) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.generation == generation && s.current == c {
		s.current = nil
	}
}

func (s *kubeSupervisor) stillWanted(generation uint64) bool {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return !s.closed && s.generation == generation
}

func (s *kubeSupervisor) setKnownNames(conf *config.Config) bool {
	next := ir.ConfiguredNames{
		Caches:         sets.New[string](nil),
		NegativeCaches: sets.New[string](nil),
		Tracers:        sets.New[string](nil),
		Rewriters:      sets.New[string](nil),
		Authenticators: sets.New[string](nil),
	}
	if conf != nil {
		for name := range conf.Caches {
			next.Caches.Set(name)
		}
		for name := range conf.NegativeCacheConfigs {
			next.NegativeCaches.Set(name)
		}
		for name := range conf.TracingOptions {
			next.Tracers.Set(name)
		}
		for name := range conf.RequestRewriters {
			next.Rewriters.Set(name)
		}
		for name := range conf.Authenticators {
			next.Authenticators.Set(name)
		}
	}
	previous := s.known.Swap(&next)
	return previous == nil ||
		!maps.Equal(previous.Caches, next.Caches) ||
		!maps.Equal(previous.NegativeCaches, next.NegativeCaches) ||
		!maps.Equal(previous.Tracers, next.Tracers) ||
		!maps.Equal(previous.Rewriters, next.Rewriters) ||
		!maps.Equal(previous.Authenticators, next.Authenticators)
}

func marshalKubeOptions(o *kubecfg.Options) []byte {
	if o == nil {
		return nil
	}
	out, err := yaml.Marshal(o)
	if err != nil {
		// there is nothing unmarshalable in the section, but a comparison
		// that cannot be made must restart rather than assume no change
		return []byte(time.Now().String())
	}
	return out
}

func bindKubeSupervisor(ctx context.Context, si *instance.ServerInstance,
	reloader reload.Reloader,
) *kubeSupervisor {
	s := newKubeSupervisor(ctx, si, reloader)
	si.OverlayProvider = s
	return s
}

func notifyKubeSupervisor(si *instance.ServerInstance, conf *config.Config) {
	if s, ok := si.OverlayProvider.(*kubeSupervisor); ok {
		s.Apply(conf, si.Tracers)
	}
}
