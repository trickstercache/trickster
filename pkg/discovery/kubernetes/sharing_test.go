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

package kubernetes

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/kube"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// watchCounter counts the watches the fake clientset opens for a resource,
// which is what the shared registry is meant to hold down: two ALBs on one
// Service should cost the API server one watch, not two.
//
// first closes once a watch has registered. A snapshot only proves the
// initial LIST completed, so an assertion on the count must wait for this
// or it races the reflector's watch.
type watchCounter struct {
	n     atomic.Int32
	first chan struct{}
	once  sync.Once
}

func countWatches(cs *fake.Clientset, resource string) *watchCounter {
	wc := &watchCounter{first: make(chan struct{})}
	cs.PrependWatchReactor(resource,
		func(action k8stesting.Action) (bool, watch.Interface, error) {
			w, err := cs.Tracker().Watch(action.GetResource(), action.GetNamespace())
			if err != nil {
				return false, nil, err
			}
			wc.n.Add(1)
			wc.once.Do(func() { close(wc.first) })
			return true, w, nil
		})
	return wc
}

// requireWatches waits for the expected number of watches to register, then
// lets the reflectors settle and asserts no more appeared
func (wc *watchCounter) requireWatches(t *testing.T, want int32, msg string) {
	t.Helper()
	require.Eventually(t, func() bool { return wc.n.Load() >= want },
		5*time.Second, 5*time.Millisecond, msg)
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, want, wc.n.Load(), msg)
}

func TestSubscriptionsWithTheSameQueryShareOneWatch(t *testing.T) {
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false)))
	watches := countWatches(cs, "endpointslices")
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop() //nolint:errcheck

	q := &do.Query{Namespace: testNS, Service: "prom", Port: "web"}
	first, second := newSnapCollector(), newSnapCollector()
	unsubA, err := d.Subscribe(q, first.handle)
	require.NoError(t, err)
	unsubB, err := d.Subscribe(q, second.handle)
	require.NoError(t, err)

	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(first.next(t)))
	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(second.next(t)))
	watches.requireWatches(t, 1,
		"both subscriptions must ride one shared informer")

	// releasing one must leave the other's informer running and delivering
	unsubA()
	updated := newSlice("prom-abc", "prom", 9090,
		endpoint("10.0.0.1", "prom-0", true, false),
		endpoint("10.0.0.2", "prom-1", true, false))
	_, err = cs.DiscoveryV1().EndpointSlices(testNS).
		Update(context.Background(), updated, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.Equal(t, []string{"10.0.0.1:9090", "10.0.0.2:9090"},
		addressesOf(second.next(t)))
	require.Equal(t, int32(1), watches.n.Load(),
		"the surviving subscription must not have reopened the watch")
	unsubB()
}

// Differing queries must not be collapsed onto one informer, or one ALB
// would see the other's members.
func TestSubscriptionsWithDifferentQueriesDoNotShare(t *testing.T) {
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false)),
		newSlice("thanos-abc", "thanos", 10902,
			endpoint("10.0.1.1", "thanos-0", true, false)))
	watches := countWatches(cs, "endpointslices")
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop() //nolint:errcheck

	prom, thanos := newSnapCollector(), newSnapCollector()
	unsubP, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom", Port: "web",
	}, prom.handle)
	require.NoError(t, err)
	defer unsubP()
	unsubT, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "thanos", Port: "web",
	}, thanos.handle)
	require.NoError(t, err)
	defer unsubT()

	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(prom.next(t)))
	require.Equal(t, []string{"10.0.1.1:10902"}, addressesOf(thanos.next(t)))
	watches.requireWatches(t, 2,
		"a different service label selector is a different watch")
}

// Two discoverers over one client are one connection, so they must share
// too: the same Service watched by discoverers in two config entries costs
// one watch.
func TestSharingSpansDiscoverers(t *testing.T) {
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false)))
	watches := countWatches(cs, "endpointslices")
	kc := kube.NewFromClientset(cs)
	q := &do.Query{Namespace: testNS, Service: "prom", Port: "web"}

	one, two := NewWithClient("one", kc), NewWithClient("two", kc)
	require.NoError(t, one.Start(t.Context()))
	require.NoError(t, two.Start(t.Context()))
	defer two.Stop() //nolint:errcheck

	a, b := newSnapCollector(), newSnapCollector()
	_, err := one.Subscribe(q, a.handle)
	require.NoError(t, err)
	_, err = two.Subscribe(q, b.handle)
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(a.next(t)))
	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(b.next(t)))
	watches.requireWatches(t, 1, "one connection, one spec, one watch")

	// stopping one discoverer entirely must not take the other's watch
	require.NoError(t, one.Stop())
	updated := newSlice("prom-abc", "prom", 9090,
		endpoint("10.0.0.2", "prom-1", true, false))
	_, err = cs.DiscoveryV1().EndpointSlices(testNS).
		Update(context.Background(), updated, metav1.UpdateOptions{})
	require.NoError(t, err)
	require.Equal(t, []string{"10.0.0.2:9090"}, addressesOf(b.next(t)))
	require.Equal(t, int32(1), watches.n.Load())
}

// The replica-group pod join uses a selector-free factory, so every
// endpointslices subscription in a namespace shares one pod watch
func TestReplicaGroupPodWatchIsShared(t *testing.T) {
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false)),
		newSlice("thanos-abc", "thanos", 10902,
			endpoint("10.0.1.1", "thanos-0", true, false)),
		zonePod("prom-0", "us-east"),
		zonePod("thanos-0", "us-west"))
	pods := countWatches(cs, "pods")
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop() //nolint:errcheck

	prom, thanos := newSnapCollector(), newSnapCollector()
	_, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom",
		Port: "web", ReplicaGroupLabel: "zone",
	}, prom.handle)
	require.NoError(t, err)
	_, err = d.Subscribe(&do.Query{
		Namespace: testNS, Service: "thanos",
		Port: "web", ReplicaGroupLabel: "zone",
	}, thanos.handle)
	require.NoError(t, err)

	require.Equal(t, "us-east", prom.next(t)[0].ReplicaGroup)
	require.Equal(t, "us-west", thanos.next(t)[0].ReplicaGroup)
	pods.requireWatches(t, 1,
		"the selector-free pod join is one watch per namespace")
}

// Preflight is the setup layer's fail-fast probe; the provider must
// delegate it to the client rather than reporting health of its own
func TestDiscovererPreflight(t *testing.T) {
	cs := fake.NewClientset()
	require.NoError(t, NewWithClient("ok", kube.NewFromClientset(cs)).
		Preflight(context.Background()))

	boom := fake.NewClientset()
	boom.PrependReactor("get", "version",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("api server unreachable")
		})
	require.ErrorContains(t,
		NewWithClient("bad", kube.NewFromClientset(boom)).
			Preflight(context.Background()),
		"api server unreachable")
}

// zonePod is a bare pod object carrying only the replica-group label the
// endpointslices join reads
func zonePod(name, zone string) *corev1.Pod {
	return &corev1.Pod{
		Name: name, Namespace: testNS,
		Labels: map[string]string{"zone": zone},
	}
}

// A named Service is filtered server-side by field selector rather than by
// listing the namespace and discarding the rest
func TestServiceKindByNameUsesAFieldSelector(t *testing.T) {
	svc := func(name string) *corev1.Service {
		return &corev1.Service{
			Name: name, Namespace: testNS,
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.96.0.1",
				Ports:     []corev1.ServicePort{{Name: "web", Port: 9090}},
			},
		}
	}
	cs := fake.NewClientset(svc("prom"), svc("thanos"))
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop() //nolint:errcheck

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Kind: do.KindService, Namespace: testNS, Service: "prom", Port: "web",
	}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.Len(t, col.next(t), 1)

	var restricted bool
	for _, a := range cs.Actions() {
		la, ok := a.(k8stesting.ListAction)
		if !ok || a.GetResource().Resource != "services" {
			continue
		}
		restricted = true
		require.Equal(t, "metadata.name=prom",
			la.GetListRestrictions().Fields.String())
	}
	require.True(t, restricted, "expected a field-selected list of services")
}

// Launch is called once by the Lifecycle; a second call (or one after Stop)
// must not start a second set of watches
func TestLaunchIsIdempotentAndEmitStopsAtStop(t *testing.T) {
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false)))
	watches := countWatches(cs, "endpointslices")
	p := &provider{name: "test", kc: kube.NewFromClientset(cs)}
	col := newSnapCollector()
	r, err := p.newSubscription(
		&do.Query{Namespace: testNS, Service: "prom", Port: "web"}, col.handle)
	require.NoError(t, err)
	s := r.(*subscription)

	s.Launch(t.Context())
	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(col.next(t)))
	s.Launch(t.Context())
	watches.requireWatches(t, 1, "a second Launch must not open a second watch")

	s.Stop()
	s.Stop()
	// a debounce timer that fires after Stop must not deliver
	s.emit()
	select {
	case snap := <-col.ch:
		t.Fatalf("emitted after stop: %v", snap)
	default:
	}

	// a subscription that was stopped before it ever launched stays quiet
	r2, err := p.newSubscription(
		&do.Query{Namespace: testNS, Service: "prom", Port: "web"}, col.handle)
	require.NoError(t, err)
	s2 := r2.(*subscription)
	s2.Stop()
	s2.Launch(t.Context())
	require.False(t, s2.launched)
}
