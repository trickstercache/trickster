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

package controller

import (
	"errors"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/events"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

// policyListKinds names the list kind of the resource for the fake dynamic client
var policyListKinds = map[schema.GroupVersionResource]string{
	cachepolicy.GVR: cachepolicy.Kind + "List",
}

func cachePolicyObject(t *testing.T, spec cachepolicy.Spec) *unstructured.Unstructured {
	t.Helper()
	u, err := cachepolicy.ToUnstructured(&cachepolicy.CachePolicy{
		Namespace: "shop", Name: "cp", Generation: 1, UID: "cp-uid",
		Spec: spec,
	})
	require.NoError(t, err)
	return u
}

func TestControllerServesCachePolicies(t *testing.T) {
	// A policy reaches the generated configuration, its verdict reaches the policy's status, and
	// what could not be done with it is an Event on the policy
	rec := &recorder{}
	p := &publisher{}
	cs := kubefake.NewClientset(ingressClass(), service(), webIngress())
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		policyListKinds, cachePolicyObject(t, cachepolicy.Spec{
			TargetRefs: []cachepolicy.TargetRef{
				{Kind: cachepolicy.KindIngress, Name: "web"},
				{Kind: cachepolicy.KindService, Name: "absent"},
			},
			Provider: "prometheus", CacheKeyParams: []string{"query"},
			ResultHeader: cachepolicy.ResultHeaderHide,
		}))
	c, err := New(Config{
		Options: options(t), Publisher: p, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(), DynamicClient: dyn,
		Recorder: events.NewWithRecorder(rec),
		ProviderPaths: func(string) po.List {
			return po.List{{
				Path: "/api/v1/query_range", HandlerName: "query_range",
				MatchTypeName: matching.PathMatchNameExact, Methods: []string{"GET"},
			}}
		},
		KnownNames: func() ir.ConfiguredNames {
			return ir.ConfiguredNames{Caches: sets.New([]string{"objects"})}
		},
	})
	require.NoError(t, err)
	t.Cleanup(c.Stop)
	require.NoError(t, c.Start(t.Context()))

	data := string(p.last().Data)
	require.Contains(t, data, "provider: prometheus")
	require.Contains(t, data, "cache_key_params:")
	require.Contains(t, data, "hide_result_header: true")
	require.Contains(t, data, "handler: query_range",
		"the provider's path is emitted under the route with the provider's handler")
	require.Contains(t, data, "path_defaults_disabled: true",
		"nothing beyond what was emitted is registered")

	eventually(t, func() bool {
		return strings.Contains(strings.Join(rec.list(), "\n"),
			"Warning TargetNotFound TricksterCachePolicy/shop/cp")
	}, "the absent target was not reported")

	eventually(t, func() bool {
		u, err := dyn.Resource(cachepolicy.GVR).Namespace("shop").Get(t.Context(), "cp",
			metav1.GetOptions{})
		if err != nil {
			return false
		}
		cp, err := cachepolicy.FromUnstructured(u)
		if err != nil || len(cp.Status.Ancestors) != 2 {
			return false
		}
		first, second := cp.Status.Ancestors[0], cp.Status.Ancestors[1]
		return string(first.ControllerName) == options(t).GatewayClassControllerName &&
			first.Conditions[0].Reason == string(gwapiv1.PolicyReasonAccepted) &&
			second.Conditions[0].Reason == string(gwapiv1.PolicyReasonTargetNotFound)
	}, "the policy's status was not written")

	// the targets are looked up on every kind a policy may name
	require.Equal(t, []string{"/api/v1/query_range"}, c.providerPathNames("prometheus"))
	require.True(t, c.targetExists(cachepolicy.KindIngress, "shop", "web"))
	require.True(t, c.targetExists(cachepolicy.KindService, "shop", "web-svc"))
	require.False(t, c.targetExists(cachepolicy.KindGateway, "shop", "gw"))
	require.False(t, c.targetExists(cachepolicy.KindHTTPRoute, "shop", "web"))
	require.False(t, c.targetExists("Deployment", "shop", "web"))
}

func TestControllerWithoutCachePolicies(t *testing.T) {
	// a cluster that does not serve the resource is served without it, and nothing is indexed
	p := &publisher{}
	c, _ := start(t, p, nil, ingressClass(), service(), webIngress())
	require.Nil(t, c.policyIndex())
	require.Nil(t, c.providerPathNames("prometheus"))
	require.Contains(t, string(p.last().Data), "kgw--ingress.shop.web_r0")
	require.NotContains(t, string(p.last().Data), "provider: prometheus")
}

func TestNewFailsWhenThePolicyResourceCannotBeRead(t *testing.T) {
	// a cluster serving the resource needs a dynamic client, which a client built over a
	// caller-supplied clientset cannot provide; and a probe the API server refuses is an error
	cs := kubefake.NewClientset()
	cs.Resources = []*metav1.APIResourceList{{
		GroupVersion: cachepolicy.GroupVersion.String(),
		APIResources: []metav1.APIResource{{Name: cachepolicy.Resource}},
	}}
	_, err := New(Config{
		Options: options(t), Publisher: &publisher{}, Client: kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.ErrorIs(t, err, kube.ErrNoRESTConfig)

	refused := kubefake.NewClientset()
	refused.PrependReactor("get", "resource", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("api server unreachable")
	})
	_, err = New(Config{
		Options: options(t), Publisher: &publisher{}, Client: kube.NewFromClientset(refused),
		GatewayClient: gwfake.NewSimpleClientset(),
	})
	require.ErrorContains(t, err, "api server unreachable")
}
