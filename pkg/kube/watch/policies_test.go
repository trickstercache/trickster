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

package watch

import (
	"testing"
	"time"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

// policyListKinds tells the fake dynamic client what a list of the resource is called, since
// nothing registers the kind in a scheme
var policyListKinds = map[schema.GroupVersionResource]string{
	cachepolicy.GVR: cachepolicy.Kind + "List",
}

func policyObject(t *testing.T, ns, name string, refs ...cachepolicy.TargetRef,
) *unstructured.Unstructured {
	t.Helper()
	p := &cachepolicy.CachePolicy{
		Namespace: ns, Name: name,
		Spec: cachepolicy.Spec{TargetRefs: refs, CacheName: "objects"},
	}
	u, err := cachepolicy.ToUnstructured(p)
	require.NoError(t, err)
	return u
}

func newDynamic(objects ...runtime.Object) dynamic.Interface {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		policyListKinds, objects...)
}

func startWithPolicies(t *testing.T, o *kubecfg.Options, dyn dynamic.Interface,
) (*Watcher, *changes) {
	t.Helper()
	c := &changes{}
	w, err := New(Config{
		Client:        kube.NewFromClientset(kubefake.NewClientset()),
		GatewayClient: gwfake.NewSimpleClientset(),
		DynamicClient: dyn,
		Options:       o,
		OnChange:      c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	return w, c
}

func TestWatcherReadsCachePolicies(t *testing.T) {
	// the resource is read through the dynamic client, listed in order, found by name, and a
	// change to one is delivered like any other kind's
	ref := cachepolicy.TargetRef{Kind: cachepolicy.KindHTTPRoute, Name: "web"}
	dyn := newDynamic(policyObject(t, "shop", "b", ref), policyObject(t, "infra", "a", ref))
	w, c := startWithPolicies(t, opts(t), dyn)
	c.await(t, 1)
	require.True(t, w.ServesCachePolicies())
	policies := w.CachePolicies()
	require.Len(t, policies, 2)
	require.Equal(t, "infra", policies[0].Namespace)
	require.Equal(t, "shop", policies[1].Namespace)
	require.Equal(t, "objects", policies[1].Spec.CacheName)
	require.Equal(t, []cachepolicy.TargetRef{ref}, policies[1].Spec.TargetRefs)

	require.NotNil(t, w.CachePolicy("shop", "b"))
	require.Nil(t, w.CachePolicy("shop", "absent"))
	require.Nil(t, w.CachePolicy("other", "b"))

	_, err := dyn.Resource(cachepolicy.GVR).Namespace("shop").Create(t.Context(),
		policyObject(t, "shop", "c", ref), metav1.CreateOptions{})
	require.NoError(t, err)
	c.await(t, 2)
	require.Eventually(t, func() bool { return len(w.CachePolicies()) == 3 },
		5*time.Second, 5*time.Millisecond)
}

func TestWatcherScopesCachePoliciesToWatchedNamespaces(t *testing.T) {
	ref := cachepolicy.TargetRef{Kind: cachepolicy.KindService, Name: "svc"}
	dyn := newDynamic(policyObject(t, "shop", "b", ref), policyObject(t, "infra", "a", ref))
	w, c := startWithPolicies(t, opts(t, func(o *kubecfg.Options) {
		o.WatchNamespaces = []string{"shop"}
	}), dyn)
	c.await(t, 1)
	require.Len(t, w.CachePolicies(), 1)
	require.Nil(t, w.CachePolicy("infra", "a"))
	require.NotNil(t, w.CachePolicy("shop", "b"))
}

func TestWatcherWithoutTheResource(t *testing.T) {
	// no dynamic client means no informer: the accessors answer nothing rather than hang
	w, c := start(t, opts(t), nil, nil)
	c.await(t, 1)
	require.False(t, w.ServesCachePolicies())
	require.Nil(t, w.CachePolicies())
	require.Nil(t, w.CachePolicy("shop", "b"))
}

func TestPolicyInformerSkipsWhatItCannotRead(t *testing.T) {
	// an object the store holds that is not a policy, or does not convert, is passed over
	dyn := newDynamic()
	p := newPolicyInformer(dyn, "shop", 0)
	require.NoError(t, p.informer.GetStore().Add(&unstructured.Unstructured{Object: map[string]any{
		"apiVersion": cachepolicy.GroupVersion.String(), "kind": cachepolicy.Kind,
		"metadata": map[string]any{"namespace": "shop", "name": "bad"},
		"spec":     map[string]any{"targetRefs": "not a list"},
	}}))
	require.NoError(t, p.informer.GetStore().Add(policyObject(t, "shop", "good")))
	require.Len(t, p.list(), 1)
	require.Nil(t, p.get("shop", "bad"))
	require.NotNil(t, p.get("shop", "good"))
	require.Nil(t, p.get("shop", "absent"))
	require.Nil(t, convertPolicy("not an object"))
	// a release before any start does not wait on a run that never happened
	p.Release()
	p.Release()
}
