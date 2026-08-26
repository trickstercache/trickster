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
// kind. See docs/autodiscovery-rbac.md.
package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
)

// SnapshotDebounce is the coalescing window for informer event bursts; a
// churning rollout produces one snapshot per window rather than one per
// endpoint event. Per-ALB damping (alb.discovery.debounce_window) layers on
// top of this.
const SnapshotDebounce = 250 * time.Millisecond

// ErrStopped is returned when subscribing to a stopped discoverer
var ErrStopped = errors.New("kubernetes discoverer is stopped")

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
func NewWithClient(name string, kc *kube.Client) discovery.Discoverer {
	return &discoverer{name: name, kc: kc}
}

type discoverer struct {
	name   string
	kc     *kube.Client
	mtx    sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	subs   map[*subscription]struct{}
	// started/stopped gate the lifecycle; subscriptions registered before
	// Start are launched by Start
	started bool
	stopped bool
}

func (d *discoverer) Start(ctx context.Context) error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return ErrStopped
	}
	if d.started {
		return nil
	}
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.started = true
	for s := range d.subs {
		s.launch(d.ctx)
	}
	return nil
}

func (d *discoverer) Stop() error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return nil
	}
	d.stopped = true
	if d.cancel != nil {
		d.cancel()
	}
	for s := range d.subs {
		s.stop()
	}
	d.subs = nil
	return nil
}

func (d *discoverer) Subscribe(q *do.Query, handler discovery.SnapshotHandler) (func(), error) {
	if q == nil || handler == nil {
		return nil, errors.New("nil query or handler")
	}
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.stopped {
		return nil, ErrStopped
	}
	s, err := d.newSubscription(q, handler)
	if err != nil {
		return nil, err
	}
	if d.subs == nil {
		d.subs = make(map[*subscription]struct{})
	}
	d.subs[s] = struct{}{}
	if d.started {
		s.launch(d.ctx)
	}
	unsubscribe := func() {
		d.mtx.Lock()
		delete(d.subs, s)
		d.mtx.Unlock()
		s.stop()
	}
	return unsubscribe, nil
}

// subscription is one query's informer set and snapshot emitter
type subscription struct {
	d       *discoverer
	q       *do.Query
	handler discovery.SnapshotHandler
	factory informers.SharedInformerFactory
	// build produces the current full-membership snapshot from the
	// kind-specific informer's lister cache
	build func() discovery.Snapshot

	// emitMtx serializes snapshot rebuild+delivery so handlers never see
	// out-of-order membership
	emitMtx sync.Mutex

	mtx sync.Mutex
	// cancel stops this subscription's informers independently of the
	// discoverer (unsubscribe); it is derived from the discoverer context
	// at launch, so factory.Shutdown can actually complete
	cancel     context.CancelFunc
	last       discovery.Snapshot
	hasLast    bool
	timer      *time.Timer
	armed      bool
	synced     bool
	stopped    bool
	launched   bool
	portWarned bool
}

// newSubscription builds the kind-appropriate informer set for the query.
// The query is expected to have been validated (and kind-defaulted) by
// config validation; kind is re-defaulted defensively for direct callers.
func (d *discoverer) newSubscription(q *do.Query, handler discovery.SnapshotHandler) (*subscription, error) {
	q = q.Clone()
	if q.Kind == "" {
		q.Kind = do.KindEndpointSlices
	}
	ns := q.Namespace
	if ns == "" {
		ns = kube.DefaultNamespace()
	}
	s := &subscription{d: d, q: q, handler: handler}

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
	opts := []informers.SharedInformerOption{informers.WithNamespace(ns)}
	if len(sel) > 0 || fieldSelector != "" {
		// a factory holds exactly one tweak func (they do not compose), so
		// label and field filtering are combined here
		var labelSelector string
		if len(sel) > 0 {
			labelSelector = labels.SelectorFromSet(sel).String()
		}
		opts = append(opts, informers.WithTweakListOptions(
			func(lo *metav1.ListOptions) {
				if labelSelector != "" {
					lo.LabelSelector = labelSelector
				}
				if fieldSelector != "" {
					lo.FieldSelector = fieldSelector
				}
			}))
	}
	s.factory = informers.NewSharedInformerFactoryWithOptions(
		d.kc.Clientset(), 0, opts...)

	var informer cache.SharedIndexInformer
	switch q.Kind {
	case do.KindEndpointSlices:
		inf := s.factory.Discovery().V1().EndpointSlices()
		informer = inf.Informer()
		lister := inf.Lister().EndpointSlices(ns)
		s.build = func() discovery.Snapshot { return s.buildEndpointSlices(lister) }
	case do.KindService:
		inf := s.factory.Core().V1().Services()
		informer = inf.Informer()
		lister := inf.Lister().Services(ns)
		s.build = func() discovery.Snapshot { return s.buildServices(lister) }
	case do.KindPods:
		inf := s.factory.Core().V1().Pods()
		informer = inf.Informer()
		lister := inf.Lister().Pods(ns)
		s.build = func() discovery.Snapshot { return s.buildPods(lister) }
	}
	if _, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { s.markDirty() },
		UpdateFunc: func(any, any) { s.markDirty() },
		DeleteFunc: func(any) { s.markDirty() },
	}); err != nil {
		return nil, err
	}
	return s, nil
}

// launch starts the subscription's informers under its own context (derived
// from the discoverer's) and emits the initial snapshot once caches sync.
// Callers hold d.mtx.
func (s *subscription) launch(ctx context.Context) {
	s.mtx.Lock()
	if s.launched || s.stopped {
		s.mtx.Unlock()
		return
	}
	s.launched = true
	subCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.mtx.Unlock()
	s.factory.Start(subCtx.Done())
	go func() {
		synced := s.factory.WaitForCacheSync(subCtx.Done())
		for typ, ok := range synced {
			if !ok {
				logger.Warn("kubernetes discovery cache did not sync",
					logging.Pairs{"discoverer": s.d.name, "type": typ.String()})
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

func (s *subscription) stop() {
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.stopped = true
	if s.timer != nil {
		s.timer.Stop()
	}
	launched := s.launched
	cancel := s.cancel
	s.mtx.Unlock()
	if cancel != nil {
		cancel()
	}
	if launched {
		// informer goroutines exit on the cancelled context; Shutdown then
		// joins them so no watch goroutine outlives the subscription
		s.factory.Shutdown()
	}
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

// emit rebuilds the snapshot from the lister cache and delivers it to the
// handler when membership actually changed
func (s *subscription) emit() {
	s.emitMtx.Lock()
	defer s.emitMtx.Unlock()
	s.mtx.Lock()
	if s.stopped {
		s.mtx.Unlock()
		return
	}
	s.armed = false
	s.mtx.Unlock()
	snap := s.build().Canonical()
	s.mtx.Lock()
	if s.stopped || (s.hasLast && snap.Equal(s.last)) {
		s.mtx.Unlock()
		return
	}
	s.last = snap
	s.hasLast = true
	s.mtx.Unlock()
	s.handler(snap)
}
