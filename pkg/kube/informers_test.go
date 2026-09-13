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

package kube

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{Namespace: ns, Name: name}
}

// Two subscribers with the same connection and spec must share one factory,
// which is the whole point: one watch per resource rather than one per
// consumer.
func TestInformerFactoryReuse(t *testing.T) {
	c := NewFromClientset(fake.NewClientset())
	spec := FactorySpec{Namespace: "monitoring"}

	a := c.InformerFactory(spec)
	defer a.Release()
	b := c.InformerFactory(spec)
	defer b.Release()

	require.Same(t, a.Factory(), b.Factory())
	require.Equal(t, 2, a.entry.refs)
	require.Equal(t, 1, entryCount(c.id), "one spec, one factory")
}

// Anything that changes which objects the API server returns must not be
// collapsed onto one factory.
func TestInformerFactoryDistinctKeys(t *testing.T) {
	c := NewFromClientset(fake.NewClientset())
	base := c.InformerFactory(FactorySpec{Namespace: "a"})
	defer base.Release()

	for _, spec := range []FactorySpec{
		{Namespace: "b"},
		{Namespace: "a", LabelSelector: "app=x"},
		{Namespace: "a", FieldSelector: "metadata.name=x"},
		{Namespace: "a", Resync: time.Minute},
	} {
		h := c.InformerFactory(spec)
		require.NotSame(t, base.Factory(), h.Factory(), "spec %+v", spec)
		h.Release()
	}

	// two clients are two connections even over the same clientset
	other := NewFromClientset(fake.NewClientset())
	h := other.InformerFactory(FactorySpec{Namespace: "a"})
	defer h.Release()
	require.NotSame(t, base.Factory(), h.Factory())
}

// Releasing one holder must leave the other's informers running; this is
// the property that lets a discoverer unsubscribe without disturbing its
// peers.
func TestInformerFactoryIndependentCancellation(t *testing.T) {
	cs := fake.NewClientset(testPod("monitoring", "p1"))
	c := NewFromClientset(cs)
	spec := FactorySpec{Namespace: "monitoring"}

	a := c.InformerFactory(spec)
	b := c.InformerFactory(spec)

	pods := b.Factory().Core().V1().Pods()
	lister := pods.Lister().Pods("monitoring")
	b.Start()
	requireSynced(t, b)

	a.Release()
	require.Equal(t, 1, b.entry.refs)
	got, err := lister.List(labels.Everything())
	require.NoError(t, err)
	require.Len(t, got, 1, "b's informer must still be serving after a released")

	b.Release()
	require.NotContains(t, informerRegistry.entries,
		factoryKey{conn: c.id, kind: factoryKindCore, spec: spec},
		"the last release removes the entry")
}

// A release after the entry is gone must be a no-op rather than a negative
// reference count on a resurrected entry.
func TestInformerFactoryReleaseIsIdempotent(t *testing.T) {
	c := NewFromClientset(fake.NewClientset())
	spec := FactorySpec{Namespace: "x"}
	h := c.InformerFactory(spec)
	first := h.Factory()
	h.Release()
	h.Release()

	// a fresh get after the last release builds a new factory, not the one
	// that was shut down
	h2 := c.InformerFactory(spec)
	defer h2.Release()
	require.NotSame(t, first, h2.Factory())
	require.Equal(t, 1, h2.entry.refs)
}

func TestInformerFactoryConcurrentGetRelease(t *testing.T) {
	c := NewFromClientset(fake.NewClientset())
	spec := FactorySpec{Namespace: "race"}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			h := c.InformerFactory(spec)
			h.Factory().Core().V1().Pods().Informer()
			h.Start()
			h.Release()
		})
	}
	wg.Wait()
	require.NotContains(t, informerRegistry.entries,
		factoryKey{conn: c.id, kind: factoryKindCore, spec: spec})
}

// A selector-bearing spec must actually reach the API server as a filter,
// not merely as part of the registry key.
func TestInformerFactoryAppliesSelectors(t *testing.T) {
	cs := fake.NewClientset(testPod("sel", "p1"))
	c := NewFromClientset(cs)
	h := c.InformerFactory(FactorySpec{
		Namespace: "sel", LabelSelector: "app=x", FieldSelector: "metadata.name=p1",
	})
	defer h.Release()
	h.Factory().Core().V1().Pods().Informer()
	h.Start()
	requireSynced(t, h)

	var listed bool
	for _, a := range cs.Actions() {
		la, ok := a.(k8stesting.ListAction)
		if !ok || a.GetResource().Resource != "pods" {
			continue
		}
		listed = true
		r := la.GetListRestrictions()
		require.Equal(t, "app=x", r.Labels.String())
		require.Equal(t, "metadata.name=p1", r.Fields.String())
	}
	require.True(t, listed, "expected the factory to list pods")
}

// requireSynced starts nothing; it waits, under a bound, for every started
// informer on the handle's factory to have synced
func requireSynced(t *testing.T, h *CoreInformerFactory) {
	t.Helper()
	stop := make(chan struct{})
	timer := time.AfterFunc(5*time.Second, func() { close(stop) })
	defer timer.Stop()
	for typ, ok := range h.WaitForCacheSync(stop) {
		require.True(t, ok, "informer %s did not sync", typ)
	}
}

// entryCount reports how many registry entries belong to a connection, so
// assertions are independent of what other tests left behind
func entryCount(conn string) int {
	informerRegistry.mtx.Lock()
	defer informerRegistry.mtx.Unlock()
	var n int
	for k := range informerRegistry.entries {
		if k.conn == conn {
			n++
		}
	}
	return n
}
