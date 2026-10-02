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

package gatewayapi

import (
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/kube"

	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
	gwapix "sigs.k8s.io/gateway-api/apisx/v1alpha1"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

func TestNewClientset(t *testing.T) {
	c, err := kube.NewFromRESTConfig(&rest.Config{Host: "https://127.0.0.1:6443"}, nil)
	require.NoError(t, err)
	gw, err := NewClientset(c)
	require.NoError(t, err)
	require.NotNil(t, gw.GatewayV1())

	// a client wrapping a caller-supplied clientset carries no connection
	// to build a second typed clientset over
	_, err = NewClientset(kube.NewFromClientset(kubefake.NewClientset()))
	require.ErrorIs(t, err, kube.ErrNoRESTConfig)
}

// The gateway-api factories join the same process-wide registry the core
// factories use, so two consumers of one spec share a watch per resource
func TestInformersAreShared(t *testing.T) {
	c, err := kube.NewFromRESTConfig(&rest.Config{Host: "https://127.0.0.1:6443"}, nil)
	require.NoError(t, err)
	cs := gwfake.NewSimpleClientset()
	spec := kube.FactorySpec{Namespace: "shop"}

	a := Informers(c, cs, spec)
	defer a.Release()
	b := Informers(c, cs, spec)
	defer b.Release()
	require.Same(t, a.Factory(), b.Factory())

	// a different spec is a different watch
	other := Informers(c, cs, kube.FactorySpec{Namespace: "infra"})
	defer other.Release()
	require.NotSame(t, a.Factory(), other.Factory())

	// a selector-bearing spec reaches the API server as a filter
	sel := Informers(c, cs, kube.FactorySpec{
		Namespace: "shop", LabelSelector: "app=x",
	})
	defer sel.Release()
	require.NotSame(t, a.Factory(), sel.Factory())

	// the handle drives the same lifecycle the core handles do
	var h kube.FactoryHandle = a
	require.NotNil(t, h)
	a.Factory().Gateway().V1().HTTPRoutes().Informer()
	a.Start()
}

// A gateway factory and a core factory for the same spec must not collide
// in the shared registry
func TestGatewayAndCoreFactoriesAreDistinct(t *testing.T) {
	c := kube.NewFromClientset(kubefake.NewClientset())
	spec := kube.FactorySpec{Namespace: "shop"}
	core := c.InformerFactory(spec)
	defer core.Release()
	gw := Informers(c, gwfake.NewSimpleClientset(), spec)
	defer gw.Release()
	require.NotNil(t, core.Factory())
	require.NotNil(t, gw.Factory())
}

// The Gateway API is an add-on, and a controller has to know whether this
// cluster serves it before it builds informers that would never sync
func TestAvailable(t *testing.T) {
	cs := kubefake.NewClientset()
	ok, err := Available(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.False(t, ok, "a cluster without the CRDs does not serve the API")

	cs.Resources = []*metav1.APIResourceList{{GroupVersion: GroupVersion}}
	ok, err = Available(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.True(t, ok)

	_, err = Available(nil)
	require.ErrorIs(t, err, kube.ErrNoConnectionOptions)
}

// The served kinds decide which informers a controller may build: a kind the
// cluster does not serve would never sync
func TestResources(t *testing.T) {
	cs := kubefake.NewClientset()
	names, ok, err := Resources(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, names)

	cs.Resources = []*metav1.APIResourceList{{
		GroupVersion: GroupVersion,
		APIResources: []metav1.APIResource{
			{Name: "httproutes"}, {Name: "gateways/status"}, {Name: "gateways"},
		},
	}}
	names, ok, err = Resources(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"gateways", "httproutes"}, names,
		"subresources are not kinds, and the list is sorted")

	_, _, err = Resources(nil)
	require.ErrorIs(t, err, kube.ErrNoConnectionOptions)
}

func TestAlphaResources(t *testing.T) {
	// the experimental channel is probed apart from the standard one, since a cluster may serve
	// the standard kinds alone
	cs := kubefake.NewClientset()
	cs.Resources = []*metav1.APIResourceList{{
		GroupVersion: GroupVersion, APIResources: []metav1.APIResource{{Name: "httproutes"}},
	}}
	names, ok, err := AlphaResources(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, names)

	cs.Resources = append(cs.Resources, &metav1.APIResourceList{
		GroupVersion: AlphaGroupVersion,
		APIResources: []metav1.APIResource{
			{Name: "udproutes"}, {Name: "tcproutes/status"}, {Name: "tcproutes"}, {Name: "tlsroutes"},
		},
	})
	names, ok, err = AlphaResources(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"tcproutes", "tlsroutes", "udproutes"}, names)
}

func TestXResources(t *testing.T) {
	// the x-k8s.io group is probed apart from both channels of the main group
	cs := kubefake.NewClientset()
	cs.Resources = []*metav1.APIResourceList{{
		GroupVersion: GroupVersion, APIResources: []metav1.APIResource{{Name: "httproutes"}},
	}}
	names, ok, err := XResources(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.False(t, ok)
	require.Nil(t, names)
	cs.Resources = append(cs.Resources, &metav1.APIResourceList{
		GroupVersion: XGroupVersion,
		APIResources: []metav1.APIResource{{Name: "xbackendtrafficpolicies"}, {Name: "xmeshes"}},
	})
	names, ok, err = XResources(kube.NewFromClientset(cs))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"xbackendtrafficpolicies", "xmeshes"}, names)
}

func TestXWatchable(t *testing.T) {
	// an informer the API server refuses never syncs, so each verb is asked in each namespace
	cs := kubefake.NewClientset()
	var asked []string
	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(a k8stesting.Action) (bool, runtime.Object, error) {
			r := a.(k8stesting.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
			attrs := r.Spec.ResourceAttributes
			asked = append(asked, attrs.Namespace+"/"+attrs.Verb)
			r.Status.Allowed = attrs.Group == gwapix.GroupName && attrs.Namespace != "denied"
			return true, r, nil
		})
	c := kube.NewFromClientset(cs)
	ok, err := XWatchable(t.Context(), c, ResourceBackendTrafficPolicies, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []string{"/list", "/watch"}, asked, "no namespaces is every namespace")
	asked = nil
	ok, err = XWatchable(t.Context(), c, ResourceBackendTrafficPolicies, []string{"shop", "denied", "data"})
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, []string{"shop/list", "shop/watch", "denied/list"}, asked)

	cs.PrependReactor("create", "selfsubjectaccessreviews",
		func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, errors.New("down") })
	_, err = XWatchable(t.Context(), c, ResourceBackendTrafficPolicies, nil)
	require.Error(t, err)
	_, err = XWatchable(t.Context(), nil, ResourceBackendTrafficPolicies, nil)
	require.ErrorIs(t, err, kube.ErrNoConnectionOptions)
}
