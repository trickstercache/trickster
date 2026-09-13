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

package events

import (
	"context"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
)

func TestReferenceForCachePolicy(t *testing.T) {
	ref := Reference(ir.Source{Kind: ir.KindCachePolicy, Namespace: "shop", Name: "p", UID: "u"})
	require.NotNil(t, ref)
	require.Equal(t, cachepolicy.GroupVersion.String(), ref.APIVersion)
	require.Equal(t, ir.KindCachePolicy, ref.Kind)
	require.Equal(t, "shop", ref.Namespace)
}

func TestReference(t *testing.T) {
	ref := Reference(ir.Source{Kind: ir.KindHTTPRoute, Namespace: "shop", Name: "web", UID: "u1"})
	require.NotNil(t, ref)
	require.Equal(t, "gateway.networking.k8s.io/v1", ref.APIVersion)
	require.Equal(t, "HTTPRoute", ref.Kind)
	require.Equal(t, "shop", ref.Namespace)
	require.EqualValues(t, "u1", ref.UID)
	for _, kind := range []string{ir.KindTCPRoute, ir.KindTLSRoute, ir.KindUDPRoute} {
		ref = Reference(ir.Source{Kind: kind, Namespace: "data", Name: "db"})
		require.NotNil(t, ref)
		require.Equal(t, "gateway.networking.k8s.io/v1alpha2", ref.APIVersion)
		require.Equal(t, kind, ref.Kind)
	}
	ref = Reference(ir.Source{Kind: ir.KindGRPCRoute, Namespace: "shop", Name: "rpc"})
	require.NotNil(t, ref)
	require.Equal(t, "gateway.networking.k8s.io/v1", ref.APIVersion)
	require.Equal(t, "GRPCRoute", ref.Kind)

	ref = Reference(ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "web"})
	require.Equal(t, "networking.k8s.io/v1", ref.APIVersion)
	ref = Reference(ir.Source{Kind: ir.KindGatewayClass, Name: "trickster"})
	require.Equal(t, "", ref.Namespace)

	require.Nil(t, Reference(ir.Source{Kind: ir.KindController, Name: "ingress"}),
		"nothing stands behind a synthesized source")
}

func TestRecorderRoutesEvents(t *testing.T) {
	fr := record.NewFakeRecorder(4)
	fr.IncludeObject = true
	r := NewWithRecorder(fr)
	// a fixed recorder has no terms to begin or end
	r.Begin(t.Context())
	r.Warning(ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "web"},
		ir.ReasonInvalidAnnotation, "bad value")
	r.Normal(ir.Source{Kind: ir.KindGatewayClass, Name: "trickster"}, "Accepted", "claimed")
	r.Warning(ir.Source{Kind: ir.KindController, Name: "ingress"}, "x", "dropped")
	require.Len(t, fr.Events, 2)
	first := <-fr.Events
	require.Contains(t, first, "Warning InvalidAnnotation bad value")
	require.Contains(t, first, "kind=Ingress")
	second := <-fr.Events
	require.Contains(t, second, "Normal Accepted claimed")
	r.Close()

	var nilRecorder *Recorder
	nilRecorder.Begin(t.Context())
	nilRecorder.Warning(ir.Source{Kind: ir.KindIngress}, "x", "y")
	nilRecorder.End()
	nilRecorder.Close()
}

func countEvents(t *testing.T, cs kubernetes.Interface) int {
	t.Helper()
	list, err := cs.CoreV1().Events("shop").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	return len(list.Items)
}

func awaitEvents(t *testing.T, cs kubernetes.Interface, want int) {
	t.Helper()
	require.Eventually(t, func() bool { return countEvents(t, cs) == want },
		5*time.Second, 10*time.Millisecond, "expected %d events", want)
}

func TestRecorderWritesToClusterWithinATerm(t *testing.T) {
	cs := fake.NewClientset()
	r := New(cs)
	src := ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "web", UID: "u1"}
	// nothing speaks for the cluster between terms
	r.Warning(src, ir.ReasonRejected, "before any term")
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, countEvents(t, cs))

	r.Begin(t.Context())
	r.Warning(src, ir.ReasonRejected, "path dropped")
	awaitEvents(t, cs, 1)
	list, err := cs.CoreV1().Events("shop").List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err)
	ev := list.Items[0]
	require.Equal(t, corev1.EventTypeWarning, ev.Type)
	require.Equal(t, ir.ReasonRejected, ev.Reason)
	require.Equal(t, "web", ev.InvolvedObject.Name)
	require.EqualValues(t, "u1", ev.InvolvedObject.UID)
	require.Equal(t, Component, ev.Source.Component)

	r.End()
	r.Warning(src, ir.ReasonRejected, "after the term")
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 1, countEvents(t, cs))

	// a new term publishes again, and beginning it twice is one term
	r.Begin(t.Context())
	r.Begin(t.Context())
	r.Normal(src, "Accepted", "again")
	awaitEvents(t, cs, 2)
	r.Close()
}

// gatedEvents holds every Event create until released or its context ends
type gatedEvents struct {
	typedcorev1.EventInterface
	entered chan struct{}
	gate    chan struct{}
}

type gatedCore struct {
	typedcorev1.CoreV1Interface
	events *gatedEvents
}

func (c gatedCore) Events(ns string) typedcorev1.EventInterface {
	return gatedEvents{
		EventInterface: c.CoreV1Interface.Events(ns), entered: c.events.entered,
		gate: c.events.gate,
	}
}

func (g gatedEvents) Create(ctx context.Context, ev *corev1.Event, opts metav1.CreateOptions,
) (*corev1.Event, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return g.EventInterface.Create(ctx, ev, opts)
}

func TestTermEndCancelsEventsInFlight(t *testing.T) {
	// The sink carries the term's context, so ending the term cancels a create
	// in flight, and what was queued behind it is dropped rather than sent later
	cs := fake.NewClientset()
	gate := &gatedEvents{entered: make(chan struct{}, 8), gate: make(chan struct{})}
	r := &Recorder{events: gatedCore{CoreV1Interface: cs.CoreV1(), events: gate}}
	ctx, cancel := context.WithCancel(t.Context())
	r.Begin(ctx)
	src := ir.Source{Kind: ir.KindIngress, Namespace: "shop", Name: "web"}
	r.Warning(src, ir.ReasonRejected, "first")
	r.Warning(src, ir.ReasonInvalidAnnotation, "second")
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no create was attempted")
	}
	cancel()
	r.End()
	close(gate.gate)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 0, countEvents(t, cs), "nothing from the ended term reaches the cluster")

	// the sink refuses outright once its term is over
	sink := &termSink{ctx: ctx, events: cs.CoreV1()}
	_, err := sink.Create(&corev1.Event{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = sink.Update(&corev1.Event{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = sink.Patch(&corev1.Event{}, nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestTermSinkCallsCarryTheTerm(t *testing.T) {
	cs := fake.NewClientset()
	sink := &termSink{ctx: t.Context(), events: cs.CoreV1()}
	ev := &corev1.Event{
		Namespace: "shop", Name: "e1",
		Reason: "Rejected", Count: 1,
	}
	created, err := sink.Create(ev)
	require.NoError(t, err)
	created.Count = 2
	updated, err := sink.Update(created)
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Count)
	patched, err := sink.Patch(updated, []byte(`{"count":3}`))
	require.NoError(t, err)
	require.EqualValues(t, 3, patched.Count)

	// once the term has been waited out, no call is admitted
	sink.wait()
	_, err = sink.Create(ev)
	require.ErrorIs(t, err, context.Canceled)
}
