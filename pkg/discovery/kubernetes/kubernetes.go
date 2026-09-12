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

// Package kubernetes implements the kubernetes autodiscovery provider on
// client-go shared informers (watch-driven; no polling). One discoverer is
// constructed per named entry in the top-level 'discovery' config section;
// each Subscribe creates a server-side-filtered informer set for its query
// (kind endpointslices, service, or pods) and emits debounced
// full-membership snapshots as the watched objects change.
//
// RBAC: the service account needs only list and watch on the resources the
// configured query kinds touch: endpointslices (discovery.k8s.io) for the
// endpointslices kind, services for the service kind, and pods for the pods
// kind. See the RBAC section of docs/alb-autodiscovery.md.
package kubernetes

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// SnapshotDebounce is the coalescing window for informer event bursts; a
// churning rollout produces one snapshot per window rather than one per
// endpoint event. Per-ALB damping (alb.discovery.debounce_window) layers on
// top of this.
const SnapshotDebounce = 250 * time.Millisecond

// ErrStopped aliases discovery.ErrStopped for callers of this package
var ErrStopped = discovery.ErrStopped

// Discoverer is the kubernetes provider's Discoverer: the shared
// Start/Stop/Subscribe lifecycle plus the API server connectivity
// preflight the setup layer runs under startup_policy: fail
type Discoverer struct {
	*discovery.Lifecycle
	kc *kube.Client
}

var _ discovery.Preflighter = &Discoverer{}

// Preflight reports whether the API server is reachable and answering
func (d *Discoverer) Preflight(ctx context.Context) error {
	return d.kc.Preflight(ctx)
}

// New constructs the kubernetes Discoverer for the provided discoverer
// options; it satisfies discovery.NewDiscovererFunc
func New(name string, o *do.Options) (discovery.Discoverer, error) {
	if o == nil {
		return nil, kube.ErrNoConnectionOptions
	}
	kc, err := kube.New(o.Kubernetes)
	if err != nil {
		return nil, err
	}
	return NewWithClient(name, kc), nil
}

// NewWithClient constructs the kubernetes Discoverer over an existing
// client; used by tests (client-go fakes) and embedders with their own
// client stack
func NewWithClient(name string, kc *kube.Client) *Discoverer {
	p := &provider{name: name, kc: kc}
	return &Discoverer{
		Lifecycle: discovery.NewLifecycle(name, p.newSubscription),
		kc:        kc,
	}
}

// provider carries the kubernetes provider's shared client; the shared
// discovery.Lifecycle owns Start/Stop/Subscribe
type provider struct {
	name string
	kc   *kube.Client
}

// subscription is one query's informer set and snapshot emitter; it
// implements discovery.SubscriptionRunner
type subscription struct {
	p       *provider
	q       *do.Query
	emitter *discovery.Emitter
	// handle is this subscription's reference to the shared informer
	// factory for its query; informer and reg are its own informer and
	// event-handler registration on it
	handle   *kube.CoreInformerFactory
	informer cache.SharedIndexInformer
	reg      cache.ResourceEventHandlerRegistration
	// podHandle references a second, selector-free shared factory joined in
	// by the endpointslices kind when replica_group_label is set: endpoints
	// carry no pod labels, so the target pod is consulted for the group
	podHandle   *kube.CoreInformerFactory
	podInformer cache.SharedIndexInformer
	podReg      cache.ResourceEventHandlerRegistration
	// podLister resolves an endpoint's TargetRef pod for label lookups;
	// nil unless podHandle is active
	podLister corelisters.PodNamespaceLister
	// build produces the current full-membership snapshot from the
	// kind-specific informer's lister cache
	build func() discovery.Snapshot

	mtx sync.Mutex
	// cancel releases this subscription independently of the discoverer
	// (unsubscribe); it is derived from the discoverer context at launch
	cancel     context.CancelFunc
	timer      *time.Timer
	armed      bool
	synced     bool
	stopped    bool
	launched   bool
	portWarned bool
}

// newSubscription builds the kind-appropriate informer set for the query;
// it satisfies discovery.NewSubscriptionFunc. The query is expected to
// have been validated (and kind-defaulted) by config validation; kind is
// re-defaulted defensively for direct callers.
func (p *provider) newSubscription(q *do.Query, handler discovery.SnapshotHandler) (discovery.SubscriptionRunner, error) {
	if q.Kind == "" {
		q.Kind = do.KindEndpointSlices
	}
	ns := q.Namespace
	if ns == "" {
		ns = kube.DefaultNamespace()
	}
	s := &subscription{p: p, q: q, emitter: discovery.NewEmitter(handler)}

	sel := labels.Set{}
	maps.Copy(sel, q.Selector)
	var fieldSelector string
	switch q.Kind {
	case do.KindEndpointSlices:
		// slices of the named service, via the well-known service-name label
		sel[discoveryv1.LabelServiceName] = q.Service
	case do.KindService:
		if q.Service != "" {
			// a single named Service, filtered server-side by field selector
			fieldSelector = fields.OneTermEqualSelector(
				"metadata.name", q.Service).String()
		}
	case do.KindPods:
	default:
		return nil, fmt.Errorf("invalid kubernetes query kind %q", q.Kind)
	}
	var labelSelector string
	if len(sel) > 0 {
		labelSelector = labels.SelectorFromSet(sel).String()
	}
	// the rendered selectors are the factory's identity, so a second
	// subscription with the same query shares this one's informers
	s.handle = p.kc.InformerFactory(kube.FactorySpec{
		Namespace:     ns,
		LabelSelector: labelSelector,
		FieldSelector: fieldSelector,
	})
	factory := s.handle.Factory()

	dirtyHandler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { s.markDirty() },
		UpdateFunc: func(any, any) { s.markDirty() },
		DeleteFunc: func(any) { s.markDirty() },
	}
	switch q.Kind {
	case do.KindEndpointSlices:
		inf := factory.Discovery().V1().EndpointSlices()
		s.informer = inf.Informer()
		lister := inf.Lister().EndpointSlices(ns)
		s.build = func() discovery.Snapshot { return s.buildEndpointSlices(lister) }
		if q.ReplicaGroupLabel != "" {
			// join the target pods so per-member replica groups can be
			// read from pod labels; a separate factory because the slice
			// factory's service-name label tweak must not filter pods
			s.podHandle = p.kc.InformerFactory(kube.FactorySpec{Namespace: ns})
			podInf := s.podHandle.Factory().Core().V1().Pods()
			s.podInformer = podInf.Informer()
			reg, err := s.podInformer.AddEventHandler(dirtyHandler)
			if err != nil {
				s.release()
				return nil, err
			}
			s.podReg = reg
			s.podLister = podInf.Lister().Pods(ns)
		}
	case do.KindService:
		inf := factory.Core().V1().Services()
		s.informer = inf.Informer()
		lister := inf.Lister().Services(ns)
		s.build = func() discovery.Snapshot { return s.buildServices(lister) }
	case do.KindPods:
		inf := factory.Core().V1().Pods()
		s.informer = inf.Informer()
		lister := inf.Lister().Pods(ns)
		s.build = func() discovery.Snapshot { return s.buildPods(lister) }
	}
	reg, err := s.informer.AddEventHandler(dirtyHandler)
	if err != nil {
		s.removeHandlers()
		s.release()
		return nil, err
	}
	s.reg = reg
	return s, nil
}

// removeHandlers deregisters this subscription's event handlers, so shared
// informers other subscriptions keep running stop calling into it
func (s *subscription) removeHandlers() {
	if s.reg != nil {
		s.informer.RemoveEventHandler(s.reg) //nolint:errcheck
		s.reg = nil
	}
	if s.podReg != nil {
		s.podInformer.RemoveEventHandler(s.podReg) //nolint:errcheck
		s.podReg = nil
	}
}

// release drops this subscription's references to the shared factories; the
// last reference to a factory stops it
func (s *subscription) release() {
	if s.handle != nil {
		s.handle.Release()
	}
	if s.podHandle != nil {
		s.podHandle.Release()
	}
}

// Launch starts the subscription's informers under its own context
// (derived from the discoverer's) and emits the initial snapshot once
// caches sync
func (s *subscription) Launch(ctx context.Context) {
	s.mtx.Lock()
	if s.launched || s.stopped {
		s.mtx.Unlock()
		return
	}
	s.launched = true
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mtx.Unlock()
	s.handle.Start()
	if s.podHandle != nil {
		s.podHandle.Start()
	}
	// the shared factories outlive any one subscription, so cancellation of
	// the discoverer context releases this subscription rather than
	// stopping the informers directly
	go func() {
		<-subCtx.Done()
		s.Stop()
	}()
	go func() {
		synced := s.handle.WaitForCacheSync(subCtx.Done())
		if s.podHandle != nil {
			maps.Copy(synced, s.podHandle.WaitForCacheSync(subCtx.Done()))
		}
		for typ, ok := range synced {
			if !ok {
				metrics.DiscoveryRefreshErrors.WithLabelValues(
					s.p.name, providers.Kubernetes).Inc()
				discovery.LogWarn("kubernetes discovery cache did not sync",
					logging.Pairs{keys.Discoverer: s.p.name, keys.Type: typ.String()})
				return
			}
		}
		s.mtx.Lock()
		s.synced = true
		s.mtx.Unlock()
		// initial emission is immediate; subsequent changes are debounced
		s.emit()
	}()
}

// Stop terminates the subscription's informers and suppresses further
// emissions
func (s *subscription) Stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
	}
	cancel := s.cancel
	s.mtx.Unlock()
	s.emitter.Stop()
	if cancel != nil {
		cancel()
	}
	// deregister before releasing: a shared informer another subscription
	// still holds keeps running, and must stop calling into this one
	s.removeHandlers()
	s.release()
}

// markDirty schedules a debounced snapshot rebuild; bursts of informer
// events within the window collapse into one emission
func (s *subscription) markDirty() {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.stopped || !s.synced || s.armed {
		return
	}
	s.armed = true
	s.timer = time.AfterFunc(SnapshotDebounce, s.emit)
}

// emit rebuilds the snapshot from the lister cache and delivers it via
// the Emitter, which serializes deliveries and suppresses no-change
// membership
func (s *subscription) emit() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.armed = false
	s.mtx.Unlock()
	s.emitter.Emit(s.build())
}
