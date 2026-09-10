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
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/mgmt"
	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/controller"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/ready"

	"github.com/stretchr/testify/require"
	kubefake "k8s.io/client-go/kubernetes/fake"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

// fakeRunner stands in for a controller; it needs no cluster
type fakeRunner struct {
	cfg      controller.Config
	started  atomic.Int32
	stopped  atomic.Int32
	resyncs  atomic.Int32
	startErr error
	// stopping counts entries into Stop, and stopBlocks holds it there,
	// which is what a controller with a pass still in flight does
	stopping   atomic.Int32
	stopBlocks chan struct{}
	// startBlocks holds Start until it is closed or the runner is stopped,
	// which is what an unreachable cluster does
	startBlocks chan struct{}
	stopped2    chan struct{}
	stopOnce    sync.Once
	// publishOnStart is offered through the controller's publisher once Start proceeds, as a
	// real controller's first reconcile does; a failed publication does not fail Start
	publishOnStart *config.Overlay
}

func (f *fakeRunner) Start(context.Context) error {
	if f.startBlocks != nil {
		// a real controller blocks here until its informer caches sync, or
		// until it is stopped
		select {
		case <-f.startBlocks:
		case <-f.stopped2:
			return f.startErr
		}
	}
	f.started.Add(1)
	if f.publishOnStart != nil && f.cfg.Publisher != nil {
		_, _ = f.cfg.Publisher.Publish(f.publishOnStart)
	}
	return f.startErr
}

func (f *fakeRunner) Stop() {
	f.stopping.Add(1)
	f.stopOnce.Do(func() {
		if f.stopped2 != nil {
			close(f.stopped2)
		}
	})
	if f.stopBlocks != nil {
		<-f.stopBlocks
	}
	f.stopped.Add(1)
}

func (f *fakeRunner) Resync() { f.resyncs.Add(1) }

func newFakeRunner() *fakeRunner {
	return &fakeRunner{stopped2: make(chan struct{})}
}

// runnerFactory hands out fake runners and records the configurations they
// were built from
type runnerFactory struct {
	mtx     sync.Mutex
	runners []*fakeRunner
	err     error
	// startErr is applied to each runner handed out, and startBlocks holds
	// each one in Start
	startErr       error
	startBlocks    chan struct{}
	publishOnStart *config.Overlay
	// attempts counts every call, including the ones that returned an error
	// and so produced no runner
	attempts atomic.Int32
}

func (f *runnerFactory) build(cfg controller.Config) (kubeRunner, error) {
	f.attempts.Add(1)
	f.mtx.Lock()
	defer f.mtx.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	r := newFakeRunner()
	r.cfg, r.startErr, r.startBlocks = cfg, f.startErr, f.startBlocks
	r.publishOnStart = f.publishOnStart
	f.runners = append(f.runners, r)
	return r, nil
}

func (f *runnerFactory) count() int {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return len(f.runners)
}

func (f *runnerFactory) at(i int) *fakeRunner {
	f.mtx.Lock()
	defer f.mtx.Unlock()
	return f.runners[i]
}

func install(t *testing.T) *runnerFactory {
	t.Helper()
	f := &runnerFactory{}
	previous := newKubeRunner
	newKubeRunner = f.build
	t.Cleanup(func() { newKubeRunner = previous })
	return f
}

func publishAs(t *testing.T, s *kubeSupervisor, o *config.Overlay) (bool, error) {
	t.Helper()
	s.mtx.Lock()
	generation := s.generation
	s.mtx.Unlock()
	return fencedPublisher{supervisor: s, generation: generation}.Publish(o)
}

// reloads counts the reloads a supervisor triggered, failing them while fail is set
type reloads struct {
	n    atomic.Int32
	fail atomic.Bool
}

func (r *reloads) reloader(string) (bool, error) {
	r.n.Add(1)
	if r.fail.Load() {
		return false, errors.New("configuration rejected")
	}
	return true, nil
}

func kubeConfig(mutate ...func(*kubecfg.Options)) *config.Config {
	c := config.NewConfig()
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeService
	for _, m := range mutate {
		m(o)
	}
	c.Kubernetes = o
	return c
}

func newTestSupervisor(t *testing.T) (*kubeSupervisor, *reloads) {
	t.Helper()
	r := &reloads{}
	s := newKubeSupervisor(t.Context(), nil, r.reloader)
	// a retry test should not wait a second for its second attempt
	s.retryMin, s.retryMax = time.Millisecond, time.Millisecond
	t.Cleanup(s.Close)
	return s, r
}

func eventually(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 5*time.Second, 5*time.Millisecond, msg)
}

func TestKubeSupervisorInertWithoutSection(t *testing.T) {
	f := install(t)
	s, r := newTestSupervisor(t)
	s.Apply(config.NewConfig(), nil)
	s.Apply(nil, nil)
	require.Nil(t, s.Overlay())
	require.Equal(t, 0, f.count())
	require.Equal(t, int32(0), r.n.Load())
}

func TestKubeSupervisorStartsOnceForOneSection(t *testing.T) {
	// The controller starts once for a section, and an unchanged section on a
	// later reload does not restart it
	f := install(t)
	s, _ := newTestSupervisor(t)
	conf := kubeConfig()
	s.Apply(conf, nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	eventually(t, func() bool {
		return f.count() > 0 && f.at(0).started.Load() == 1
	}, "controller not started")
	// a fresh but identical section, as a file reload produces
	s.Apply(kubeConfig(), nil)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, 1, f.count(), "an unchanged section must not restart")
}

func TestKubeSupervisorRestartsOnChange(t *testing.T) {
	// Everything the controller holds comes from the section, so a changed
	// section is a clean restart rather than an in-place update
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "other" }), nil)
	eventually(t, func() bool { return f.count() == 2 }, "controller not restarted")
	eventually(t, func() bool { return f.at(0).stopped.Load() == 1 },
		"the outgoing controller was not stopped")
	require.Equal(t, "other", f.at(1).cfg.Options.IngressClass)
}

func TestKubeSupervisorUnloadsWhenDisabled(t *testing.T) {
	// Turning the section off has to take the generated routes out of service,
	// which only a reload onto an empty overlay does
	f := install(t)
	s, r := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	_, err := publishAs(t, s, &config.Overlay{Version: "v1"})
	require.NoError(t, err)
	require.Equal(t, "v1", s.Overlay().VersionString())

	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.Enabled = new(false) }), nil)
	eventually(t, func() bool { return s.Overlay() == nil },
		"the generated configuration was not withdrawn")
	eventually(t, func() bool { return f.at(0).stopped.Load() == 1 },
		"the controller was not stopped")
	require.Equal(t, int32(2), r.n.Load(),
		"withdrawing the configuration must reload the daemon")
}

func TestKubeSupervisorPublishStoresBeforeReload(t *testing.T) {
	// Publish stores the overlay before reloading, so the reload it triggers
	// reads the overlay it is being run for
	var s *kubeSupervisor
	var seen string
	s = newKubeSupervisor(t.Context(), nil, func(string) (bool, error) {
		seen = s.Overlay().VersionString()
		return true, nil
	})
	t.Cleanup(s.Close)
	_, err := publishAs(t, s, &config.Overlay{Version: "v9"})
	require.NoError(t, err)
	require.Equal(t, "v9", seen)
}

func TestKubeSupervisorRetriesConstruction(t *testing.T) {
	// A controller that cannot be built is retried, because the usual cause is
	// an API server that is briefly unreachable
	f := install(t)
	f.mtx.Lock()
	f.err = errors.New("no cluster")
	f.mtx.Unlock()
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	// a second attempt is what proves the retry loop, not just the first try
	eventually(t, func() bool { return f.attempts.Load() >= 2 },
		"a controller that could not be built was not retried")
	require.Equal(t, 0, f.count())
}

func TestKubeSupervisorProgramsReadinessOnPublication(t *testing.T) {
	// A pod is ready once a translation is serving, not once the controller has started: a rejected
	// first publication leaves the wait, a later success clears it, a failed start programs nothing
	f := install(t)
	f.publishOnStart = &config.Overlay{Version: "v1"}
	si := &instance.ServerInstance{Readiness: &ready.State{}}
	r := &reloads{}
	r.fail.Store(true)
	s := newKubeSupervisor(t.Context(), si, r.reloader)
	s.retryMin, s.retryMax = time.Millisecond, time.Millisecond
	t.Cleanup(s.Close)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 && f.at(0).started.Load() == 1 },
		"controller not started")
	eventually(t, func() bool { return r.n.Load() >= 1 }, "the first translation was not offered")
	require.True(t, si.Readiness.Pending(), "a rejected first translation must not program readiness")
	require.Nil(t, s.Overlay(), "a rejected overlay is not kept")

	r.fail.Store(false)
	_, err := publishAs(t, s, &config.Overlay{Version: "v2"})
	require.NoError(t, err)
	require.False(t, si.Readiness.Pending(), "the translation that was applied programs readiness")

	f.mtx.Lock()
	f.startErr = errors.New("cache sync failed")
	f.publishOnStart = nil
	f.mtx.Unlock()
	si.Readiness.SetPending()
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "other" }), nil)
	eventually(t, func() bool { return f.count() >= 3 }, "the failing controller was not retried")
	require.True(t, si.Readiness.Pending(), "a controller that has not started programs nothing")
}

func TestKubeSupervisorReadinessAcrossDisableAndEnable(t *testing.T) {
	// Turning the section off leaves nothing to wait for; turning it back on holds the pod until
	// the new controller publishes, unless a predecessor's overlay still serves in the meantime
	f := install(t)
	f.startBlocks = make(chan struct{})
	si := &instance.ServerInstance{Readiness: &ready.State{}}
	s := newKubeSupervisor(t.Context(), si, (&reloads{}).reloader)
	t.Cleanup(s.Close)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not built")
	require.True(t, si.Readiness.Pending(), "installing a first controller holds readiness")
	close(f.startBlocks)
	_, err := publishAs(t, s, &config.Overlay{Version: "v1"})
	require.NoError(t, err)
	require.False(t, si.Readiness.Pending())

	// a replacement keeps serving what its predecessor published
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "other" }), nil)
	eventually(t, func() bool { return f.count() == 2 && f.at(1).started.Load() == 1 },
		"controller not restarted")
	require.False(t, si.Readiness.Pending(), "a replacement must not withdraw readiness")

	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.Enabled = new(false) }), nil)
	eventually(t, func() bool { return s.Overlay() == nil }, "the configuration was not withdrawn")
	require.False(t, si.Readiness.Pending(), "with no controller there is nothing to wait for")

	f.mtx.Lock()
	f.startBlocks = make(chan struct{})
	f.mtx.Unlock()
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 3 }, "controller not rebuilt")
	require.True(t, si.Readiness.Pending(), "re-enabling with nothing serving holds readiness")
	f.mtx.Lock()
	close(f.startBlocks)
	f.mtx.Unlock()
	_, err = publishAs(t, s, &config.Overlay{Version: "v2"})
	require.NoError(t, err)
	require.False(t, si.Readiness.Pending())
}

func TestKubeSupervisorReadinessFollowsRealControllerPublication(t *testing.T) {
	// The real controller's Start returns after its first reconcile whether or not the daemon took
	// the translation; readiness follows the publication, so a rejected overlay keeps the pod waiting
	previous := newKubeRunner
	newKubeRunner = func(cfg controller.Config) (kubeRunner, error) {
		cfg.Client = kube.NewFromClientset(kubefake.NewClientset())
		cfg.GatewayClient = gwfake.NewSimpleClientset()
		return controller.New(cfg)
	}
	t.Cleanup(func() { newKubeRunner = previous })
	si := &instance.ServerInstance{Readiness: &ready.State{}}
	r := &reloads{}
	r.fail.Store(true)
	s := newKubeSupervisor(t.Context(), si, r.reloader)
	t.Cleanup(s.Close)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return r.n.Load() >= 1 }, "the first translation was not offered")
	s.mtx.Lock()
	current := s.current
	s.mtx.Unlock()
	require.NotNil(t, current, "the controller started despite the rejected translation")
	require.True(t, si.Readiness.Pending(), "a rejected first translation must not program readiness")

	r.fail.Store(false)
	current.Resync()
	eventually(t, func() bool { return !si.Readiness.Pending() },
		"a later translation the daemon took did not program readiness")
	require.NotNil(t, s.Overlay())
}

func TestKubeSupervisorRetriesStart(t *testing.T) {
	// A controller that fails to start is retried too, and is not left as the
	// current one where Close would stop it a second time
	f := install(t)
	f.mtx.Lock()
	f.startErr = errors.New("cache sync failed")
	f.mtx.Unlock()
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() >= 2 },
		"a controller that failed to start was not retried")
	require.Equal(t, int32(1), f.at(0).stopped.Load(),
		"a failed start must stop the controller exactly once")
	s.mtx.Lock()
	require.Nil(t, s.current)
	s.mtx.Unlock()
}

func TestKubeSupervisorDiscardsStaleStart(t *testing.T) {
	// A newer configuration arriving mid-start wins; the stale controller is
	// stopped rather than left running unreferenced
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.mtx.Lock()
	s.generation = 99
	s.mtx.Unlock()
	require.False(t, s.adopt(1, newFakeRunner()))
	require.True(t, s.adopt(99, newFakeRunner()))
	require.False(t, s.stillWanted(1))
	require.True(t, s.stillWanted(99))
	require.Equal(t, 0, f.count())
}

func TestKubeSupervisorCloseIsIdempotent(t *testing.T) {
	// Close is safe to call twice, which shutdown and a racing reload can do
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	s.Close()
	s.Close()
	require.Equal(t, int32(1), f.at(0).stopped.Load())
	// applying after close must not start anything
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "later" }), nil)
	require.Equal(t, 1, f.count())
}

func TestBindAndNotifyKubeSupervisor(t *testing.T) {
	// bindKubeSupervisor makes the supervisor the instance's overlay provider,
	// which is how the reload path finds the generated configuration
	f := install(t)
	si := &instance.ServerInstance{}
	s := bindKubeSupervisor(t.Context(), si, func(string) (bool, error) {
		return true, nil
	})
	t.Cleanup(s.Close)
	require.Same(t, s, si.OverlayProvider)
	notifyKubeSupervisor(si, kubeConfig())
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	// an instance with some other provider is left alone
	notifyKubeSupervisor(&instance.ServerInstance{}, kubeConfig())
}

func TestKubeSupervisorNilOverlay(t *testing.T) {
	// A nil supervisor is what an instance without one reads as
	var s *kubeSupervisor
	require.Nil(t, s.Overlay())
}

func TestMarshalKubeOptions(t *testing.T) {
	require.Nil(t, marshalKubeOptions(nil))
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeService
	require.Equal(t, marshalKubeOptions(o), marshalKubeOptions(o.Clone()))
	other := o.Clone()
	other.IngressClass = "changed"
	require.NotEqual(t, marshalKubeOptions(o), marshalKubeOptions(other))
}

func TestKubeReloadSourceIsNotRateLimited(t *testing.T) {
	// The reload source the controller uses is not the operator's, so it is not
	// subject to the operator reload rate limit
	require.False(t, reload.IsUserRequested(reload.SourceKubernetes))
}

func TestKubeSupervisorReportsUnloadFailure(t *testing.T) {
	// A reload that fails while withdrawing the configuration is reported; the
	// controller is gone either way
	f := install(t)
	var failing atomic.Bool
	s := newKubeSupervisor(t.Context(), nil, func(string) (bool, error) {
		if failing.Load() {
			return false, errors.New("reload failed")
		}
		return true, nil
	})
	s.retryMin, s.retryMax = time.Millisecond, time.Millisecond
	t.Cleanup(s.Close)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	failing.Store(true)
	s.Apply(config.NewConfig(), nil)
	eventually(t, func() bool { return f.at(0).stopped.Load() == 1 },
		"the controller was not stopped")
	require.Nil(t, s.Overlay())
}

func TestKubeSupervisorStopsOvertakenController(t *testing.T) {
	// A controller that a newer configuration overtook while it was being built
	// is stopped rather than left running unreferenced
	f := install(t)
	s, _ := newTestSupervisor(t)
	var once sync.Once
	previous := newKubeRunner
	newKubeRunner = func(cfg controller.Config) (kubeRunner, error) {
		// a reload lands between building this controller and adopting it
		once.Do(func() {
			s.Apply(kubeConfig(func(o *kubecfg.Options) {
				o.IngressClass = "newer"
			}), nil)
		})
		return previous(cfg)
	}
	t.Cleanup(func() { newKubeRunner = previous })
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool {
		return f.count() > 0 && f.at(0).stopped.Load() == 1
	}, "the overtaken controller was not stopped")
	require.Equal(t, int32(0), f.at(0).started.Load(),
		"an overtaken controller must never be started")
}

func TestKubeSupervisorPublishesIntoTheRunningConfig(t *testing.T) {
	// The whole chain end to end: the supervisor stores an overlay, reloads the daemon onto
	// it, and the generated backend is live in the running configuration
	install(t)
	dir := t.TempDir()
	path := writeConfig(t, dir, reloadableConfig(0))
	si := startOverlayInstance(t, path)
	reloader := func(source string) (bool, error) {
		return Reload(si, source, "-config", path)
	}
	si.Reloader = reloader
	s := newKubeSupervisor(t.Context(), si, reloader)
	si.OverlayProvider = s
	t.Cleanup(s.Close)

	changed, err := publishAs(t, s, &config.Overlay{
		Data: []byte(overlayTestBackend), Prefix: overlayTestPrefix,
		Version: "kgw-1",
	})
	require.NoError(t, err)
	require.True(t, changed, "a new overlay must reload the daemon")
	require.NotNil(t, si.Config.Backends[overlayTestBackendName],
		"the generated backend is not in the applied configuration")
	require.NotNil(t, si.Backends[overlayTestBackendName],
		"the generated backend has no running client")
	require.Equal(t, "kgw-1", si.Config.OverlayVersion())

	// withdrawing it takes the generated routes out of service
	changed, err = publishAs(t, s, &config.Overlay{
		Prefix: overlayTestPrefix, Version: "kgw-2"})
	require.NoError(t, err)
	require.True(t, changed)
	require.Nil(t, si.Config.Backends[overlayTestBackendName])
}

func TestKubeSupervisorFencesRetiredPublishers(t *testing.T) {
	// A controller finishing a pass after its generation was replaced must not restore the
	// routes of a scope this process no longer has; ordinary resyncs would not repair it
	f := install(t)
	s, r := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	retired := f.at(0).cfg.Publisher

	// the new generation publishes what it translated
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "newer" }), nil)
	eventually(t, func() bool { return f.count() == 2 }, "controller not restarted")
	_, err := f.at(1).cfg.Publisher.Publish(&config.Overlay{Version: "new"})
	require.NoError(t, err)
	require.Equal(t, "new", s.Overlay().VersionString())

	// the outgoing one finishes afterwards, and is refused
	before := r.n.Load()
	_, err = retired.Publish(&config.Overlay{Version: "old"})
	require.ErrorIs(t, err, controller.ErrRetired)
	require.Equal(t, "new", s.Overlay().VersionString(),
		"a retired generation replaced the running configuration")
	require.Equal(t, before, r.n.Load(), "a retired generation reloaded")
}

func TestKubeSupervisorFencesPublishAfterDisable(t *testing.T) {
	// A controller retired by disabling the section must not restore the routes the disable
	// reload just removed; with no controller left, no resync would ever repair it
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	retired := f.at(0).cfg.Publisher
	_, err := retired.Publish(&config.Overlay{Version: "v1"})
	require.NoError(t, err)

	s.Apply(config.NewConfig(), nil)
	eventually(t, func() bool { return s.Overlay() == nil },
		"the generated configuration was not withdrawn")
	_, err = retired.Publish(&config.Overlay{Version: "v2"})
	require.ErrorIs(t, err, controller.ErrRetired)
	require.Nil(t, s.Overlay(),
		"a retired controller restored the configuration that was unloaded")
}

func TestKubeSupervisorFencesDelayedUnload(t *testing.T) {
	// A disable that takes a while to run must not clear an overlay a newer,
	// enabled generation has since published
	install(t)
	s, _ := newTestSupervisor(t)
	// the generation the unload belongs to is two behind the current one
	s.mtx.Lock()
	s.generation = 5
	s.mtx.Unlock()
	s.overlay.Store(&config.Overlay{Version: "live"})
	s.unload(3)
	require.Equal(t, "live", s.Overlay().VersionString(),
		"a stale unload cleared a newer generation's configuration")
	s.unload(5)
	require.Nil(t, s.Overlay())
}

func TestKubeSupervisorRollsBackRejectedOverlay(t *testing.T) {
	// An overlay the daemon rejected must not be left in place: every later reload merges it,
	// so one unloadable route would freeze every configuration change, disabling included
	install(t)
	var reject atomic.Bool
	var reloads atomic.Int32
	s := newKubeSupervisor(t.Context(), nil, func(string) (bool, error) {
		reloads.Add(1)
		if reject.Load() {
			return false, errors.New("configuration is invalid")
		}
		return true, nil
	})
	t.Cleanup(s.Close)
	good := &config.Overlay{Version: "good"}
	_, err := publishAs(t, s, good)
	require.NoError(t, err)

	reject.Store(true)
	_, err = publishAs(t, s, &config.Overlay{Version: "bad"})
	require.Error(t, err)
	require.Equal(t, "good", s.Overlay().VersionString(),
		"a rejected overlay is merged into every later reload")

	// and the good one is still what a later reload sees
	reject.Store(false)
	_, err = publishAs(t, s, &config.Overlay{Version: "next"})
	require.NoError(t, err)
	require.Equal(t, "next", s.Overlay().VersionString())
}

func TestKubeSupervisorResyncsOnCacheChange(t *testing.T) {
	// A cache the operator adds later has to reach a controller that is already
	// running, or a route naming it stays rejected until the next resync
	f := install(t)
	s, _ := newTestSupervisor(t)
	conf := kubeConfig()
	s.Apply(conf, nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	require.Equal(t, int32(0), f.at(0).resyncs.Load())
	require.NotNil(t, s.knownNames().Caches)

	// the same section with a new negative cache restarts nothing, but the
	// running controller is asked to translate again
	withCache := kubeConfig()
	withCache.NegativeCacheConfigs = negative.ConfigLookup{"api-errors": nil}
	s.Apply(withCache, nil)
	eventually(t, func() bool { return f.at(0).resyncs.Load() == 1 },
		"the controller was never asked to reconsider")
	require.Equal(t, 1, f.count(), "an added cache must not restart the controller")
	require.True(t, s.knownNames().NegativeCaches.Contains("api-errors"))

	// an unchanged configuration asks for nothing
	s.Apply(withCache, nil)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, int32(1), f.at(0).resyncs.Load())
}

func TestKubeSupervisorSharesCertificateState(t *testing.T) {
	// Every generation shares one certificate inventory, so a controller built
	// from a new configuration can withdraw what its predecessor installed
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "newer" }), nil)
	eventually(t, func() bool { return f.count() == 2 }, "controller not restarted")
	require.NotNil(t, f.at(0).cfg.CertState)
	require.Same(t, f.at(0).cfg.CertState, f.at(1).cfg.CertState)
}

// overlayTestBadCache names a cache the configuration does not define, which
// is what a mistyped cache-name annotation used to compile to
const overlayTestBadCache = `
backends:
  ` + overlayTestBackendName + `:
    provider: rpc
    origin_url: http://example.com
    cache_name: nonexistent
`

func TestKubeSupervisorRecoversFromARejectedOverlay(t *testing.T) {
	// The recovery the rollback exists for, against the real loader: a rejected overlay must
	// not be merged into the reloads that follow, or one bad route would freeze them all
	install(t)
	dir := t.TempDir()
	path := writeConfig(t, dir, reloadableConfig(0))
	si := startOverlayInstance(t, path)
	reloader := func(source string) (bool, error) {
		return Reload(si, source, "-config", path)
	}
	si.Reloader = reloader
	s := newKubeSupervisor(t.Context(), si, reloader)
	si.OverlayProvider = s
	t.Cleanup(s.Close)

	_, err := publishAs(t, s, &config.Overlay{
		Data: []byte(overlayTestBackend), Prefix: overlayTestPrefix,
		Version: "kgw-good",
	})
	require.NoError(t, err)
	require.NotNil(t, si.Config.Backends[overlayTestBackendName])

	_, err = publishAs(t, s, &config.Overlay{
		Data: []byte(overlayTestBadCache), Prefix: overlayTestPrefix,
		Version: "kgw-bad",
	})
	require.Error(t, err, "an overlay naming an undefined cache must be rejected")
	require.Equal(t, "kgw-good", s.Overlay().VersionString(),
		"the rejected overlay was left as what every later reload merges")

	// an unrelated file reload has to succeed, which it cannot do if the
	// rejected overlay is still what is merged
	writeConfig(t, dir, reloadableConfig(0)+"\n# unrelated change\n")
	ok, err := Reload(si, reload.SourceSIGHUP, "-config", path)
	require.NoError(t, err, "a file reload must not be blocked by a rejected overlay")
	require.True(t, ok)
	require.NotNil(t, si.Config.Backends[overlayTestBackendName],
		"the last accepted overlay must still be in force")

	// and so must turning the controller off
	s.unloadForTest(t)
	require.Nil(t, si.Config.Backends[overlayTestBackendName])
}

func (s *kubeSupervisor) unloadForTest(t *testing.T) {
	// unloadForTest withdraws the generated configuration as the current
	// generation, which is what disabling the section does
	t.Helper()
	s.mtx.Lock()
	generation := s.generation
	s.mtx.Unlock()
	s.unload(generation)
}

func TestKubeSupervisorSerializesOverlappingTransitions(t *testing.T) {
	// A transition clears the current runner before waiting for it to stop, so a second reload
	// during the wait must not start a controller alongside one still finishing a pass
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")

	// the first controller is slow to stop, which is what a pass already in
	// flight makes it
	a := f.at(0)
	a.stopBlocks = make(chan struct{})

	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "b" }), nil)
	eventually(t, func() bool { return a.stopping.Load() == 1 },
		"the outgoing controller was never stopped")

	// a third configuration lands while the first is still stopping
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "c" }), nil)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, f.count(),
		"a controller was started while an older one was still stopping")

	close(a.stopBlocks)
	eventually(t, func() bool { return f.count() == 2 }, "no controller started")
	// the transition that lost the race must not have started one of its own
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 2, f.count(),
		"an overtaken transition started a controller as well")
	require.Equal(t, "c", f.at(1).cfg.Options.IngressClass,
		"the newest configuration must be the one running")
	require.Equal(t, int32(0), f.at(1).stopped.Load())
}

func TestKubeSupervisorRetiresTheLiveController(t *testing.T) {
	// A transition retires what is running when it runs, not when its configuration was applied,
	// or a transition still starting its controller would leave it running beside a later one
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool {
		return f.count() > 0 && f.at(0).started.Load() == 1
	}, "controller not started")

	// two configurations land back to back, the second while the first is
	// still in its transition
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "b" }), nil)
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "c" }), nil)
	eventually(t, func() bool {
		n := f.count()
		return n >= 2 && f.at(n-1).started.Load() == 1 &&
			f.at(n-1).cfg.Options.IngressClass == "c"
	}, "no controller was started for the newest configuration")
	time.Sleep(50 * time.Millisecond)

	// exactly one controller may be left running
	var live int
	for i := range f.count() {
		r := f.at(i)
		if r.started.Load() > 0 && r.stopped.Load() == 0 {
			live++
		}
	}
	require.Equal(t, 1, live, "more than one controller was left running")
	last := f.at(f.count() - 1)
	require.Equal(t, "c", last.cfg.Options.IngressClass)
	require.Zero(t, last.stopped.Load())
}

func TestKubeSupervisorIsNotBlockedByAnUnreachableCluster(t *testing.T) {
	// A controller that cannot reach its cluster blocks in Start; that must not
	// hold up the configuration that corrects it, nor shutdown
	f := install(t)
	s, _ := newTestSupervisor(t)
	f.mtx.Lock()
	f.startBlocks = make(chan struct{})
	f.mtx.Unlock()
	s.Apply(kubeConfig(), nil)
	eventually(t, func() bool { return f.count() == 1 }, "controller not built")

	// the corrected configuration must not wait for the stuck one
	f.mtx.Lock()
	f.startBlocks = nil
	f.mtx.Unlock()
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "reachable" }), nil)
	eventually(t, func() bool {
		return f.count() == 2 && f.at(1).started.Load() == 1
	}, "a stuck controller froze the configuration that corrects it")
	require.Equal(t, int32(1), f.at(0).stopped.Load(),
		"stopping the stuck controller is what unblocks it")
}

func TestKubeSupervisorSkipsBuildingAnOvertakenGeneration(t *testing.T) {
	// A controller overtaken while it was waiting to retry is not built at all;
	// building one only to stop it helps nobody, and it must not be retried
	f := install(t)
	s, _ := newTestSupervisor(t)
	s.mtx.Lock()
	s.generation = 7
	s.mtx.Unlock()
	opts := kubeConfig().Kubernetes

	c, err := s.build(3, opts)
	require.NoError(t, err)
	require.Nil(t, c, "an overtaken generation must build nothing")

	require.False(t, s.start(3, opts), "an overtaken generation must not retry")
	require.Equal(t, 0, f.count())
	require.Nil(t, currentRunner(s))
}

func TestKubeSupervisorKnownNamesBeforeAnyConfig(t *testing.T) {
	// A supervisor that has not been told about a configuration yet reports no
	// names, which accepts any of them rather than rejecting every one
	install(t)
	s, _ := newTestSupervisor(t)
	require.Nil(t, s.knownNames().Caches)
	require.Nil(t, s.knownNames().NegativeCaches)
}

func TestKubeSupervisorStaleTransitionCannotRetireANewerController(t *testing.T) {
	// Transitions are not lock-ordered, so an obsolete one can run after its replacement
	// finished; it must not stop the controller it finds, or the process is left with none
	tests := []struct {
		name string
		opts func() *kubecfg.Options
	}{
		{"replacement", func() *kubecfg.Options { return kubeConfig().Kubernetes }},
		// the same transition arriving late as a disable
		{"disable", func() *kubecfg.Options { return nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := install(t)
			s, _ := newTestSupervisor(t)
			s.Apply(kubeConfig(), nil)
			eventually(t, func() bool {
				return f.count() == 1 && f.at(0).started.Load() == 1
			}, "the first controller never started")
			stale := currentGeneration(s)

			live := kubeConfig(func(o *kubecfg.Options) { o.IngressClass = "c" })
			s.Apply(live, nil)
			eventually(t, func() bool {
				return f.count() == 2 && f.at(1).started.Load() == 1
			}, "the replacement controller never started")

			// the transition the first Apply queued finally runs
			s.swap(stale, test.opts())

			require.Zero(t, f.at(1).stopped.Load(),
				"a stale transition stopped the live controller")
			require.Equal(t, 2, f.count(),
				"a stale transition started a controller of its own")
			require.Same(t, f.at(1), currentRunner(s),
				"the live controller is no longer the current one")

			// it is still the one watching, and a later reload of the same
			// section is still the no-op it should be
			currentRunner(s).Resync()
			require.Equal(t, int32(1), f.at(1).resyncs.Load())
			s.Apply(live, nil)
			time.Sleep(20 * time.Millisecond)
			require.Equal(t, 2, f.count())
			require.Zero(t, f.at(1).stopped.Load())
		})
	}
}

func currentGeneration(s *kubeSupervisor) uint64 {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.generation
}

func currentRunner(s *kubeSupervisor) kubeRunner {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	return s.current
}

func TestKubeSupervisorPublishesTracer(t *testing.T) {
	// The controller's spans go to the tracer defaults.tracing_name names, looked up per pass
	// so a reload that rebuilds the tracers does not leave it reporting to a retired one
	f := install(t)
	s, _ := newTestSupervisor(t)
	otlp := &tracing.Tracer{Name: "otlp"}
	tracers := tracing.Tracers{"otlp": otlp}
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.Defaults.TracingName = "otlp" }), tracers)
	eventually(t, func() bool { return f.count() == 1 }, "controller not started")
	lookup := f.at(0).cfg.Tracer
	require.NotNil(t, lookup)
	require.Same(t, otlp, lookup())

	replacement := &tracing.Tracer{Name: "otlp"}
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.Defaults.TracingName = "otlp" }),
		tracing.Tracers{"otlp": replacement})
	require.Same(t, replacement, lookup(), "an unchanged section keeps the controller, "+
		"which must see the rebuilt tracer")
	s.Apply(kubeConfig(), tracers)
	require.Nil(t, lookup(), "a section naming no tracer traces nothing")
	s.Apply(kubeConfig(func(o *kubecfg.Options) { o.Defaults.TracingName = "gone" }), tracers)
	require.Nil(t, lookup())
}

func TestProviderPaths(t *testing.T) {
	// every time series provider's predefined paths are read from its own client, so a route
	// served through it can be refused a path the provider handles itself
	for _, name := range providers.HTTPTimeSeriesProviderNames() {
		require.NotEmpty(t, providerPaths(name), name)
	}
	var names []string
	for _, p := range providerPaths(providers.Prometheus) {
		names = append(names, p.Path)
	}
	require.Contains(t, names, "/api/v1/query_range")
	require.Nil(t, providerPaths(providers.ReverseProxyCacheShort))
	require.Nil(t, providerPaths(providers.MySQL))

	// a provider whose client cannot be built contributes nothing rather than failing startup
	failing := rt.Lookup{
		providers.Prometheus: func(string, *bo.Options, http.Handler, cache.Cache,
			backends.Backends, rt.Lookup,
		) (backends.Backend, error) {
			return nil, errors.New("no client")
		},
	}
	require.Empty(t, readProviderPaths(failing))
}

func TestStartReadinessWaitsForController(t *testing.T) {
	// With a kubernetes section, the readiness endpoint reports not programmed from the moment
	// the listeners answer until the controller's first translation is applied, then ready
	f := install(t)
	f.mtx.Lock()
	f.startBlocks = make(chan struct{})
	f.publishOnStart = &config.Overlay{Version: "v1"}
	f.mtx.Unlock()
	guardSignals(t)
	port := availablePort(t)
	path := writeConfig(t, t.TempDir(), fmt.Sprintf(`
listeners:
  default:
    address: 127.0.0.1
    port: %d
  mgmt:
    port: 0
  metrics:
    port: 0
backends:
  test:
    provider: rp
    origin_url: http://127.0.0.1:1
mgmt:
  shutdown_drain_timeout: 1s
kubernetes:
  defaults:
    routing_mode: service
`, port))
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() { errs <- Start(ctx, "-config", path) }()
	waitForPort(t, port)
	eventually(t, func() bool { return f.count() == 1 }, "controller not built")
	status, body := getReady(t, port)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, ready.BodyNotProgrammed, body)

	close(f.startBlocks)
	eventually(t, func() bool {
		status, _ := getReady(t, port)
		return status == http.StatusOK
	}, "readiness did not report ready once the controller had started")
	cancel()
	select {
	case err := <-errs:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after cancellation")
	}
}

func getReady(t *testing.T, port int) (int, string) {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, mgmt.DefaultReadyHandlerPath))
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}
