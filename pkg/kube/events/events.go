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

// Package events publishes Kubernetes Events against the objects the controller claims, so what
// it could not do with an object is visible on that object rather than only in the log
package events

import (
	"context"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// Component is the source every Event names
const Component = "trickster-gateway-controller"

// Recorder publishes Events through a broadcaster that lives one leadership term: Begin binds
// every API call to the term's context, and End discards whatever is still queued
type Recorder struct {
	events typedcorev1.EventsGetter
	// fixed is a recorder supplied whole, which tests use; it has no terms
	fixed record.EventRecorder

	mtx         sync.Mutex
	rec         record.EventRecorder
	broadcaster record.EventBroadcaster
	sink        *termSink
	cancel      context.CancelFunc
	stopSink    func()
}

// New builds a Recorder that writes Events through the clientset once a term begins
func New(cs kubernetes.Interface) *Recorder {
	return &Recorder{events: cs.CoreV1()}
}

// NewWithRecorder wraps an existing recorder, which is how tests capture Events
func NewWithRecorder(rec record.EventRecorder) *Recorder {
	return &Recorder{fixed: rec}
}

// Begin starts a leadership term: a broadcaster bound to the term's context, whose sink carries
// that context into every API call, so the term ending cancels the calls in flight
func (r *Recorder) Begin(ctx context.Context) {
	if r == nil || r.fixed != nil {
		return
	}
	r.End()
	termCtx, cancel := context.WithCancel(ctx)
	sink := &termSink{ctx: termCtx, events: r.events}
	b := record.NewBroadcaster(record.WithContext(termCtx))
	w := b.StartRecordingToSink(sink)
	r.mtx.Lock()
	r.broadcaster, r.sink, r.cancel = b, sink, cancel
	r.stopSink = w.Stop
	r.rec = b.NewRecorder(scheme.Scheme, corev1.EventSource{Component: Component})
	r.mtx.Unlock()
}

// End ends the term: the calls in flight are cancelled and waited for, and Events not yet
// sent are discarded rather than sent under a successor
func (r *Recorder) End() {
	if r == nil {
		return
	}
	r.mtx.Lock()
	b, sink, cancel, stop := r.broadcaster, r.sink, r.cancel, r.stopSink
	r.broadcaster, r.sink, r.cancel, r.stopSink, r.rec = nil, nil, nil, nil, nil
	r.mtx.Unlock()
	if b == nil {
		return
	}
	cancel()
	// the sink is detached before the broadcaster drains, so what it holds is dropped
	stop()
	sink.wait()
	b.Shutdown()
}

// Close ends the current term, if any
func (r *Recorder) Close() {
	r.End()
}

// Warning publishes a Warning Event against the source object
func (r *Recorder) Warning(src ir.Source, reason, message string) {
	r.event(src, corev1.EventTypeWarning, reason, message)
}

// Normal publishes a Normal Event against the source object
func (r *Recorder) Normal(src ir.Source, reason, message string) {
	r.event(src, corev1.EventTypeNormal, reason, message)
}

func (r *Recorder) event(src ir.Source, typ, reason, message string) {
	if r == nil {
		return
	}
	rec := r.fixed
	if rec == nil {
		r.mtx.Lock()
		rec = r.rec
		r.mtx.Unlock()
	}
	if rec == nil {
		// between terms nothing speaks for the cluster
		return
	}
	ref := Reference(src)
	if ref == nil {
		return
	}
	rec.Event(ref, typ, reason, message)
}

// termSink writes Events with the term's context, so a lost Lease cancels
// the call in flight and every retry after it
type termSink struct {
	ctx    context.Context
	events typedcorev1.EventsGetter
	// inflight counts the calls started, so ending the term can wait for them to return;
	// ended refuses any call starting after that wait began
	mtx      sync.Mutex
	inflight sync.WaitGroup
	ended    bool
}

func (s *termSink) enter() error {
	// a call is admitted unless the term has ended, and an admitted call must call leave
	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.ended {
		return context.Canceled
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.inflight.Add(1)
	return nil
}

func (s *termSink) leave() {
	s.inflight.Done()
}

func (s *termSink) wait() {
	// the term ends here, and the wait returns once every call in flight has returned
	s.mtx.Lock()
	s.ended = true
	s.mtx.Unlock()
	s.inflight.Wait()
}

func (s *termSink) Create(e *corev1.Event) (*corev1.Event, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	return s.events.Events(e.Namespace).Create(s.ctx, e, metav1.CreateOptions{})
}

func (s *termSink) Update(e *corev1.Event) (*corev1.Event, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	return s.events.Events(e.Namespace).Update(s.ctx, e, metav1.UpdateOptions{})
}

func (s *termSink) Patch(e *corev1.Event, data []byte) (*corev1.Event, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	return s.events.Events(e.Namespace).Patch(s.ctx, e.Name, types.StrategicMergePatchType,
		data, metav1.PatchOptions{})
}

// Reference returns the object reference an Event on the source is attached to, or nil for a
// source no Kubernetes object stands behind
func Reference(src ir.Source) *corev1.ObjectReference {
	var apiVersion string
	switch src.Kind {
	case ir.KindGateway, ir.KindGatewayClass, ir.KindHTTPRoute, ir.KindGRPCRoute,
		ir.KindBackendTLSPolicy:
		apiVersion = gwapiv1.GroupVersion.String()
	case ir.KindTCPRoute, ir.KindTLSRoute, ir.KindUDPRoute:
		apiVersion = gwapiv1a2.GroupVersion.String()
	case ir.KindIngress, ir.KindIngressClass:
		apiVersion = netv1.SchemeGroupVersion.String()
	case ir.KindCachePolicy:
		apiVersion = cachepolicy.GroupVersion.String()
	default:
		return nil
	}
	return &corev1.ObjectReference{
		APIVersion: apiVersion, Kind: src.Kind,
		Namespace: src.Namespace, Name: src.Name, UID: types.UID(src.UID),
	}
}
