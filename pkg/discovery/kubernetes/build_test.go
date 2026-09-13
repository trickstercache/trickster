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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	"k8s.io/client-go/tools/cache"
)

// newIndexer seeds a lister-backing store with the provided objects
func newIndexer(t *testing.T, objs ...any) cache.Indexer {
	t.Helper()
	idx := cache.NewIndexer(cache.MetaNamespaceKeyFunc,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, o := range objs {
		require.NoError(t, idx.Add(o))
	}
	return idx
}

// testSubscription is a subscription detached from any informer, for
// exercising the snapshot builders directly
func testSubscription(q *do.Query) *subscription {
	return &subscription{p: &provider{name: "test"}, q: q}
}

// A Service with no allocated address cannot be dialed, so it must not
// enter the pool as a member with an empty host.
func TestBuildServicesSkipsUnaddressable(t *testing.T) {
	svc := func(name, clusterIP string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: corev1.ServiceSpec{
				ClusterIP: clusterIP,
				Ports:     []corev1.ServicePort{{Name: "web", Port: 80}},
			},
		}
	}
	idx := newIndexer(t,
		svc("headless", corev1.ClusterIPNone),
		svc("unallocated", ""),
		svc("normal", "10.96.0.1"))
	s := testSubscription(&do.Query{Namespace: testNS, Port: "web"})
	snap := s.buildServices(
		discoveryNamespaceServices(idx))
	require.Equal(t, []string{"10.96.0.1:80"}, addressesOf(snap))
	require.Equal(t, discovery.ReadyUnknown, snap[0].Ready,
		"a Service conveys no readiness")
}

// A pod being deleted is already draining; leaving it in the pool sends
// traffic to a container that is on its way down.
func TestBuildPodsSkipsTerminating(t *testing.T) {
	pod := func(name, ip string, deleting bool) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 9090}}}}},
			Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
		}
		if deleting {
			now := metav1.Now()
			p.DeletionTimestamp = &now
			p.Finalizers = []string{"test/hold"}
		}
		return p
	}
	idx := newIndexer(t, pod("live", "10.0.0.1", false),
		pod("terminating", "10.0.0.2", true))
	s := testSubscription(&do.Query{Namespace: testNS, Port: "web"})
	snap := s.buildPods(corelisters.NewPodLister(idx).Pods(testNS))
	require.Equal(t, []string{"10.0.0.1:9090"}, addressesOf(snap))
	require.Equal(t, discovery.NotReady, snap[0].Ready,
		"a pod with no Ready condition is not ready")
}

// An endpoint with no address is a slot the control plane has not filled
// in yet, not a member.
func TestBuildEndpointSlicesSkipsAddresslessAndAmbiguousPorts(t *testing.T) {
	slice := newSlice("prom-abc", "prom", 9090,
		endpoint("10.0.0.1", "prom-0", true, false),
		discoveryv1.Endpoint{Conditions: discoveryv1.EndpointConditions{
			Ready: new(true)}},
	)
	// an endpoint with no TargetRef is named by its address
	slice.Endpoints = append(slice.Endpoints, discoveryv1.Endpoint{
		Addresses: []string{"10.0.0.9"},
	})
	ambiguous := newSlice("prom-def", "prom", 9090)
	ambiguous.Ports = []discoveryv1.EndpointPort{
		{Name: new("a"), Port: new(int32(1))},
		{Name: new("b"), Port: new(int32(2))},
	}
	ambiguous.Endpoints = []discoveryv1.Endpoint{
		endpoint("10.0.0.5", "prom-5", true, false)}

	idx := newIndexer(t, slice, ambiguous)
	s := testSubscription(&do.Query{Namespace: testNS, Service: "prom"})
	snap := s.buildEndpointSlices(
		discoverylisters.NewEndpointSliceLister(idx).EndpointSlices(testNS))
	require.Equal(t, []string{"10.0.0.1:9090", "10.0.0.9:9090"},
		addressesOf(snap), "the addressless endpoint and ambiguous slice are skipped")
	require.Equal(t, "10.0.0.9", snap[1].Name,
		"an endpoint with no target pod is named by its address")
	require.Equal(t, discovery.Ready, snap[1].Ready,
		"a nil Ready condition means ready, per the EndpointSlice API")
}

// The port warning fires once per build, so a namespace full of
// misconfigured objects yields one line rather than one per object
func TestWarnPortIsOncePerBuild(t *testing.T) {
	s := testSubscription(&do.Query{Port: "nope"})
	s.warnPort("service", "svc-a", 2)
	require.True(t, s.portWarned)
	s.warnPort("service", "svc-b", 2)
	require.True(t, s.portWarned)
	// a clean rebuild rearms it
	s.clearPortWarn()
	require.False(t, s.portWarned)
}

// discoveryNamespaceServices is a small helper so the Service builder call
// reads like the other two
func discoveryNamespaceServices(idx cache.Indexer) corelisters.ServiceNamespaceLister {
	return corelisters.NewServiceLister(idx).Services(testNS)
}

// An unresolvable port means the query names a port the object does not
// declare; those objects are skipped rather than dialed on port 0.
func TestBuildersSkipUnresolvablePorts(t *testing.T) {
	svcIdx := newIndexer(t, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: testNS},
		Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.1", Ports: []corev1.ServicePort{
			{Name: "a", Port: 1}, {Name: "b", Port: 2}}},
	})
	s := testSubscription(&do.Query{Namespace: testNS})
	require.Empty(t, s.buildServices(discoveryNamespaceServices(svcIdx)))

	podIdx := newIndexer(t, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: testNS},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Ports: []corev1.ContainerPort{
				{Name: "a", ContainerPort: 1}, {Name: "b", ContainerPort: 2}}}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
	})
	p := testSubscription(&do.Query{Namespace: testNS})
	require.Empty(t, p.buildPods(corelisters.NewPodLister(podIdx).Pods(testNS)))
}

// A port entry with no number carries no information, so it neither
// resolves nor counts toward ambiguity
func TestResolveSlicePortIgnoresNumberlessPorts(t *testing.T) {
	ports := []discoveryv1.EndpointPort{
		{Name: new("unset")},
		{Name: new("web"), Port: new(int32(8080))},
	}
	p, _, ok := resolveSlicePort(ports, "")
	require.True(t, ok, "one usable port is not ambiguous")
	require.Equal(t, int32(8080), p)

	_, _, ok = resolveSlicePort([]discoveryv1.EndpointPort{{Name: new("unset")}}, "")
	require.False(t, ok, "no usable port at all")
}

// A numeric query port the Service does not declare is still dialed: the
// operator asked for a specific port on the ClusterIP
func TestResolveServicePortUndeclaredNumeric(t *testing.T) {
	p, app, ok := resolveServicePort(
		[]corev1.ServicePort{{Name: "web", Port: 80}}, "8443")
	require.True(t, ok)
	require.Equal(t, int32(8443), p)
	require.Empty(t, app)
}
