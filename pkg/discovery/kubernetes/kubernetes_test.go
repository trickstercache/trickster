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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/kube"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const testNS = "monitoring"

// snapCollector captures emitted snapshots for assertions
type snapCollector struct {
	ch chan discovery.Snapshot
}

func newSnapCollector() *snapCollector {
	return &snapCollector{ch: make(chan discovery.Snapshot, 16)}
}

func (c *snapCollector) handle(s discovery.Snapshot) {
	c.ch <- s
}

// next waits for the next emitted snapshot
func (c *snapCollector) next(t *testing.T) discovery.Snapshot {
	t.Helper()
	select {
	case s := <-c.ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for snapshot")
		return nil
	}
}

func addressesOf(s discovery.Snapshot) []string {
	out := make([]string, len(s))
	for i, m := range s {
		out[i] = m.Address
	}
	return out
}

func newSlice(name, svc string, port int32, eps ...discoveryv1.Endpoint) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: testNS,
			Labels:    map[string]string{discoveryv1.LabelServiceName: svc},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports: []discoveryv1.EndpointPort{
			{Name: new("web"), Port: new(port)},
		},
		Endpoints: eps,
	}
}

func endpoint(ip, podName string, ready, terminating bool) discoveryv1.Endpoint {
	return discoveryv1.Endpoint{
		Addresses: []string{ip},
		Conditions: discoveryv1.EndpointConditions{
			Ready:       new(ready),
			Terminating: new(terminating),
		},
		TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: podName},
	}
}

func TestEndpointSlicesDiscovery(t *testing.T) {
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false),
			endpoint("10.0.0.2", "prom-1", false, false), // not yet ready
			endpoint("10.0.0.3", "prom-2", true, true),   // terminating: omitted
		),
		// another namespace: excluded by the namespace-scoped informer
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: "prom-other", Namespace: "elsewhere",
				Labels: map[string]string{discoveryv1.LabelServiceName: "prom"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Ports: []discoveryv1.EndpointPort{
				{Name: new("web"), Port: new(int32(9090))}},
			Endpoints: []discoveryv1.Endpoint{
				endpoint("10.9.9.9", "prom-x", true, false)},
		},
	)
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom", Port: "web",
	}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Equal(t, []string{"10.0.0.1:9090", "10.0.0.2:9090"},
		addressesOf(snap), "terminating and other-namespace endpoints omitted")
	require.Equal(t, "prom-0", snap[0].Name)
	require.Equal(t, discovery.Ready, snap[0].Ready)
	require.Equal(t, discovery.NotReady, snap[1].Ready)
	require.Equal(t, "http", snap[0].Scheme)
	require.Equal(t, testNS, snap[0].Labels["namespace"])
	require.Equal(t, "prom", snap[0].Labels["service"])

	// a rolling restart: prom-1 becomes ready, prom-0 starts terminating
	updated := newSlice("prom-abc", "prom", 9090,
		endpoint("10.0.0.1", "prom-0", true, true),
		endpoint("10.0.0.2", "prom-1", true, false),
	)
	_, err = cs.DiscoveryV1().EndpointSlices(testNS).
		Update(context.Background(), updated, metav1.UpdateOptions{})
	require.NoError(t, err)

	snap = col.next(t)
	require.Equal(t, []string{"10.0.0.2:9090"}, addressesOf(snap),
		"terminating member removed before pod deletion")
	require.Equal(t, discovery.Ready, snap[0].Ready)
}

func TestServiceDiscovery(t *testing.T) {
	cs := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: "prom-a", Namespace: testNS,
				Labels:      map[string]string{"app": "prom"},
				Annotations: map[string]string{AnnotationScheme: "https"},
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: "10.96.0.10",
				Ports:     []corev1.ServicePort{{Name: "web", Port: 9090}},
			},
		},
		&corev1.Service{ // headless: skipped
			ObjectMeta: metav1.ObjectMeta{
				Name: "prom-headless", Namespace: testNS,
				Labels: map[string]string{"app": "prom"},
			},
			Spec: corev1.ServiceSpec{
				ClusterIP: corev1.ClusterIPNone,
				Ports:     []corev1.ServicePort{{Name: "web", Port: 9090}},
			},
		},
	)
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Kind: do.KindService, Namespace: testNS,
		Selector: map[string]string{"app": "prom"}, Port: "web",
	}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "prom-a", snap[0].Name)
	require.Equal(t, "10.96.0.10:9090", snap[0].Address)
	require.Equal(t, "https", snap[0].Scheme,
		"scheme annotation honored when the query sets none")
	require.Equal(t, discovery.ReadyUnknown, snap[0].Ready)
}

func TestPodsDiscovery(t *testing.T) {
	pod := func(name, ip string, ready bool) *corev1.Pod {
		cond := corev1.ConditionFalse
		if ready {
			cond = corev1.ConditionTrue
		}
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: testNS,
				Labels: map[string]string{"app": "prom"},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 9090}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning, PodIP: ip,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: cond}},
			},
		}
	}
	pending := pod("prom-pending", "", false)
	pending.Status.Phase = corev1.PodPending

	cs := fake.NewClientset(
		pod("prom-0", "10.0.0.1", true),
		pod("prom-1", "10.0.0.2", false),
		pending,
	)
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Kind: do.KindPods, Namespace: testNS,
		Selector: map[string]string{"app": "prom"},
	}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Equal(t, []string{"10.0.0.1:9090", "10.0.0.2:9090"},
		addressesOf(snap), "pending pod without an IP omitted")
	require.Equal(t, discovery.Ready, snap[0].Ready)
	require.Equal(t, discovery.NotReady, snap[1].Ready)

	// deleting a pod removes its member
	require.NoError(t, cs.CoreV1().Pods(testNS).
		Delete(context.Background(), "prom-0", metav1.DeleteOptions{}))
	snap = col.next(t)
	require.Equal(t, []string{"10.0.0.2:9090"}, addressesOf(snap))
}

func TestSubscribeStoppedAndLifecycle(t *testing.T) {
	cs := fake.NewClientset()
	d := NewWithClient("test", kube.NewFromClientset(cs))

	// subscribing before Start is permitted; the subscription launches on
	// Start and delivers an initial (empty) snapshot
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom", Port: "web"}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.NoError(t, d.Start(t.Context()))
	require.Empty(t, col.next(t))

	require.NoError(t, d.Stop())
	_, err = d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom"}, col.handle)
	require.ErrorIs(t, err, ErrStopped)
	require.NoError(t, d.Stop(), "Stop is idempotent")

	_, err = d.Subscribe(nil, nil)
	require.Error(t, err)
}

func TestPodsReplicaGroupLabel(t *testing.T) {
	pod := func(name, ip, replica string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: testNS,
				Labels: map[string]string{
					"app":                "prom",
					"prometheus/replica": replica,
				},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Ports: []corev1.ContainerPort{{Name: "web", ContainerPort: 9090}},
			}}},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning, PodIP: ip,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			},
		}
	}
	cs := fake.NewClientset(
		pod("prom-shard0-a", "10.0.0.1", "shard-0"),
		pod("prom-shard0-b", "10.0.0.2", "shard-0"),
		pod("prom-shard1-a", "10.0.0.3", "shard-1"),
	)
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Kind: do.KindPods, Namespace: testNS,
		Selector:          map[string]string{"app": "prom"},
		ReplicaGroupLabel: "prometheus/replica",
	}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 3)
	groups := map[string]string{}
	for _, m := range snap {
		groups[m.Name] = m.ReplicaGroup
	}
	require.Equal(t, "shard-0", groups["prom-shard0-a"])
	require.Equal(t, "shard-0", groups["prom-shard0-b"])
	require.Equal(t, "shard-1", groups["prom-shard1-a"])
}

func TestEndpointSlicesReplicaGroupLabel(t *testing.T) {
	// the slice's endpoints reference target pods; groups come from the
	// pods via the joined pod informer
	mkPod := func(name, replica string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: testNS,
			Labels: map[string]string{"prometheus/replica": replica},
		}}
	}
	cs := fake.NewClientset(
		newSlice("prom-abc", "prom", 9090,
			endpoint("10.0.0.1", "prom-0", true, false),
			endpoint("10.0.0.2", "prom-1", true, false),
			endpoint("10.0.0.3", "prom-orphan", true, false),
		),
		mkPod("prom-0", "shard-0"),
		mkPod("prom-1", "shard-1"),
		// prom-orphan has no pod object: it joins without a group
	)
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom", Port: "web",
		ReplicaGroupLabel: "prometheus/replica",
	}, col.handle)
	require.NoError(t, err)
	defer unsub()

	snap := col.next(t)
	require.Len(t, snap, 3)
	groups := map[string]string{}
	for _, m := range snap {
		groups[m.Name] = m.ReplicaGroup
	}
	require.Equal(t, "shard-0", groups["prom-0"])
	require.Equal(t, "shard-1", groups["prom-1"])
	require.Empty(t, groups["prom-orphan"],
		"an endpoint with no resolvable pod joins without a group")
}

func TestNewConstructorErrors(t *testing.T) {
	_, err := New("d", nil)
	require.Error(t, err, "nil options")
	// nil kubernetes connection block
	_, err = New("d", &do.Options{Provider: "kubernetes"})
	require.Error(t, err)
	// in-cluster outside a cluster
	_, err = New("d", &do.Options{Provider: "kubernetes",
		Kubernetes: &do.KubernetesOptions{InCluster: true}})
	require.Error(t, err)
}

func TestAmbiguousPortSkipsObjects(t *testing.T) {
	slice := newSlice("prom-abc", "prom", 9090,
		endpoint("10.0.0.1", "prom-0", true, false))
	slice.Ports = append(slice.Ports, discoveryv1.EndpointPort{
		Name: new("metrics"), Port: new(int32(9091))})
	cs := fake.NewClientset(slice)
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()

	col := newSnapCollector()
	// no port in the query, two declared ports: ambiguous; the slice is
	// skipped (with a warn-once log) and the membership is empty
	unsub, err := d.Subscribe(&do.Query{
		Namespace: testNS, Service: "prom"}, col.handle)
	require.NoError(t, err)
	defer unsub()
	require.Empty(t, col.next(t))
}

func TestServiceReplicaGroupLabel(t *testing.T) {
	cs := fake.NewClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prom-a", Namespace: testNS,
			Labels: map[string]string{"app": "prom", "shard": "s0"},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.96.0.10",
			Ports:     []corev1.ServicePort{{Name: "web", Port: 9090}},
		},
	})
	d := NewWithClient("test", kube.NewFromClientset(cs))
	require.NoError(t, d.Start(t.Context()))
	defer d.Stop()
	col := newSnapCollector()
	unsub, err := d.Subscribe(&do.Query{
		Kind: do.KindService, Namespace: testNS,
		Selector: map[string]string{"app": "prom"}, Port: "web",
		ReplicaGroupLabel: "shard",
	}, col.handle)
	require.NoError(t, err)
	defer unsub()
	snap := col.next(t)
	require.Len(t, snap, 1)
	require.Equal(t, "s0", snap[0].ReplicaGroup)
}

func TestWarnBuildCountsError(t *testing.T) {
	s := &subscription{p: &provider{name: "d"}, q: &do.Query{}}
	// direct invocation: lister errors are unreachable with fakes
	s.warnBuild("synthetic list error", context.DeadlineExceeded)
}

func TestStartAfterStop(t *testing.T) {
	d := NewWithClient("test", kube.NewFromClientset(fake.NewClientset()))
	require.NoError(t, d.Stop())
	require.ErrorIs(t, d.Start(t.Context()), ErrStopped)
}

func TestSubscribeInvalidKind(t *testing.T) {
	d := NewWithClient("test", kube.NewFromClientset(fake.NewClientset()))
	_, err := d.Subscribe(&do.Query{Kind: "deployments"},
		func(discovery.Snapshot) {})
	require.Error(t, err)
}
