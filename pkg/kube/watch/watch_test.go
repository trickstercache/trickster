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
	"context"
	"sync/atomic"
	"testing"
	"time"

	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

// changes counts debounced OnChange deliveries
type changes struct{ n atomic.Int32 }

func (c *changes) handler() func() { return func() { c.n.Add(1) } }

func (c *changes) await(t *testing.T, want int32) {
	t.Helper()
	require.Eventually(t, func() bool { return c.n.Load() >= want },
		5*time.Second, 5*time.Millisecond,
		"expected at least %d change deliveries, saw %d", want, c.n.Load())
}

func opts(t *testing.T, mutate ...func(*kubecfg.Options)) *kubecfg.Options {
	t.Helper()
	o := kubecfg.New()
	o.Defaults.RoutingMode = kubecfg.RoutingModeService
	// a short window keeps the tests quick; the debounce itself is asserted
	// separately
	o.DebounceWindow = 1
	for _, m := range mutate {
		m(o)
	}
	require.NoError(t, o.Validate())
	return o
}

func svc(ns, name string) *corev1.Service {
	return &corev1.Service{
		Namespace: ns, Name: name,
	}
}

func tlsSecret(ns, name string) *corev1.Secret {
	return &corev1.Secret{
		Namespace: ns, Name: name,
		Type: corev1.SecretTypeTLS,
	}
}

func ing(ns, name string) *netv1.Ingress {
	return &netv1.Ingress{
		Namespace: ns, Name: name,
	}
}

func httpRoute(ns, name string) *gwapiv1.HTTPRoute {
	return &gwapiv1.HTTPRoute{
		Namespace: ns, Name: name,
	}
}

func gw(ns, name string) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		Namespace: ns, Name: name,
	}
}

// gatewayGVR is the Gateway resource, named explicitly because the fake tracker pluralizes the
// kind to "gatewaies" and files seeded objects where the typed client never reads
var gatewayGVR = schema.GroupVersionResource{
	Group: gwapiv1.GroupName, Version: "v1", Resource: "gateways",
}

func seedGateways(t *testing.T, cs *gwfake.Clientset, gateways ...*gwapiv1.Gateway) {
	t.Helper()
	for _, g := range gateways {
		require.NoError(t, cs.Tracker().Create(gatewayGVR, g, g.Namespace))
	}
}

func start(t *testing.T, o *kubecfg.Options, core []runtime.Object,
	gwObjs []runtime.Object, gateways ...*gwapiv1.Gateway,
) (*Watcher, *changes) {
	t.Helper()
	c := &changes{}
	gwcs := gwfake.NewSimpleClientset(gwObjs...)
	seedGateways(t, gwcs, gateways...)
	w, err := New(Config{
		Client:        kube.NewFromClientset(kubefake.NewClientset(core...)),
		GatewayClient: gwcs,
		Options:       o,
		OnChange:      c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	require.True(t, w.HasSynced())
	return w, c
}

func TestWatcherReadsEveryKind(t *testing.T) {
	// Every kind the translators read must be in cache once the watcher starts,
	// and the first change must be delivered without waiting for an event
	core := []runtime.Object{
		svc("shop", "web"), svc("infra", "api"),
		tlsSecret("shop", "tls"),
		ing("shop", "site"),
		&netv1.IngressClass{Name: "trickster"},
	}
	gwObjs := []runtime.Object{
		httpRoute("shop", "web"),
		&gwapiv1.GRPCRoute{Namespace: "shop", Name: "rpc"},
		&gwapiv1.GatewayClass{Name: "trickster"},
		&gwapiv1.ReferenceGrant{
			Namespace: "shop", Name: "grant",
		},
		&gwapiv1.BackendTLSPolicy{
			Namespace: "shop", Name: "tls",
		},
		&gwapiv1a2.TCPRoute{Namespace: "data", Name: "db"},
		&gwapiv1a2.TLSRoute{Namespace: "data", Name: "shop"},
		&gwapiv1a2.UDPRoute{Namespace: "data", Name: "dns"},
	}
	w, c := start(t, opts(t), core, gwObjs, gw("infra", "gw"))
	c.await(t, 1)
	require.Len(t, w.BackendTLSPolicies(), 1)
	require.Len(t, w.TCPRoutes(), 1)
	require.Len(t, w.TLSRoutes(), 1)
	require.Len(t, w.UDPRoutes(), 1)
	require.NotNil(t, w.TCPRoute("data", "db"))
	require.NotNil(t, w.TLSRoute("data", "shop"))
	require.NotNil(t, w.UDPRoute("data", "dns"))
	require.Nil(t, w.TCPRoute("data", "absent"))
	require.Nil(t, w.TLSRoute("data", "absent"))
	require.Nil(t, w.UDPRoute("data", "absent"))

	require.Len(t, w.Services(), 2)
	require.Len(t, w.Secrets(), 1)
	require.Len(t, w.Ingresses(), 1)
	require.Len(t, w.IngressClasses(), 1)
	require.Len(t, w.Gateways(), 1)
	require.Len(t, w.HTTPRoutes(), 1)
	require.Len(t, w.GRPCRoutes(), 1)
	require.NotNil(t, w.GRPCRoute("shop", "rpc"))
	require.Nil(t, w.GRPCRoute("shop", "absent"))
	require.Len(t, w.GatewayClasses(), 1)
	require.Len(t, w.ReferenceGrants(), 1)

	// results are ordered so a rebuild is deterministic
	require.Equal(t, "infra", w.Services()[0].Namespace)
	require.Equal(t, "shop", w.Services()[1].Namespace)

	require.NotNil(t, w.Service("shop", "web"))
	require.Nil(t, w.Service("shop", "absent"))
	require.NotNil(t, w.Secret("shop", "tls"))
	require.Nil(t, w.Secret("shop", "absent"))
}

func TestWatcherSkipsUnservedGatewayKinds(t *testing.T) {
	// A Gateway API kind the cluster does not serve gets no informer, since one would never
	// sync; the accessor answers nothing rather than dereferencing a lister never built
	gwcs := gwfake.NewSimpleClientset(&gwapiv1.BackendTLSPolicy{
		Namespace: "shop", Name: "tls",
	},
		&gwapiv1.GRPCRoute{Namespace: "shop", Name: "rpc"},
		&gwapiv1a2.TCPRoute{Namespace: "data", Name: "db"},
		&gwapiv1a2.TLSRoute{Namespace: "data", Name: "shop"},
		&gwapiv1a2.UDPRoute{Namespace: "data", Name: "dns"})
	c := &changes{}
	w, err := New(Config{
		Client:           kube.NewFromClientset(kubefake.NewClientset()),
		GatewayClient:    gwcs,
		GatewayResources: []string{"gatewayclasses", "gateways", "httproutes", "referencegrants"},
		AlphaResources:   []string{"tlsroutes"},
		Options:          opts(t),
		OnChange:         c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	c.await(t, 1)
	require.Empty(t, w.BackendTLSPolicies())
	require.Empty(t, w.GRPCRoutes())
	require.Nil(t, w.GRPCRoute("shop", "rpc"))
	require.Empty(t, w.TCPRoutes())
	require.Nil(t, w.TCPRoute("data", "db"))
	require.Empty(t, w.UDPRoutes())
	require.Nil(t, w.UDPRoute("data", "dns"))
	require.Len(t, w.TLSRoutes(), 1, "the one experimental kind served is watched")
	require.NotNil(t, w.TLSRoute("data", "shop"))
	require.True(t, w.ServesGatewayAPI())
}

func TestWatcherDeliversOnChange(t *testing.T) {
	// A change to any watched kind must reach the handler
	cs := kubefake.NewClientset(svc("shop", "web"))
	gwcs := gwfake.NewSimpleClientset()
	c := &changes{}
	w, err := New(Config{
		Client: kube.NewFromClientset(cs), GatewayClient: gwcs,
		Options: opts(t), OnChange: c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	c.await(t, 1)

	_, err = cs.CoreV1().Services("shop").Create(context.Background(),
		svc("shop", "second"), metav1.CreateOptions{})
	require.NoError(t, err)
	c.await(t, 2)
	require.Eventually(t, func() bool { return len(w.Services()) == 2 },
		5*time.Second, 5*time.Millisecond)
}

func TestWatcherDebouncesBursts(t *testing.T) {
	// A burst of changes must collapse into one rebuild; that is the whole point
	// of the debounce window
	cs := kubefake.NewClientset()
	c := &changes{}
	w, err := New(Config{
		Client:        kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
		Options: opts(t, func(o *kubecfg.Options) {
			o.DebounceWindow = timeconv.Duration(300 * time.Millisecond)
		}),
		OnChange: c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	c.await(t, 1)
	afterSync := c.n.Load()

	for i := range 20 {
		_, err = cs.CoreV1().Services("shop").Create(context.Background(),
			svc("shop", "s"+string(rune('a'+i))), metav1.CreateOptions{})
		require.NoError(t, err)
	}
	c.await(t, afterSync+1)
	time.Sleep(600 * time.Millisecond)
	require.LessOrEqual(t, c.n.Load(), afterSync+2,
		"20 objects created together must not produce 20 rebuilds")
}

func TestWatcherWatchesOnlyTLSSecrets(t *testing.T) {
	// Only TLS secrets are watched: a controller reads TLS material and nothing
	// else, and holding every Secret in a cluster is neither necessary nor safe
	cs := kubefake.NewClientset(
		tlsSecret("shop", "tls"),
		&corev1.Secret{
			Namespace: "shop", Name: "app-creds",
			Type: corev1.SecretTypeOpaque,
		})
	w, err := New(Config{
		Client:        kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
		Options:       opts(t),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))

	var listed bool
	for _, a := range cs.Actions() {
		la, ok := a.(k8stesting.ListAction)
		if !ok || a.GetResource().Resource != "secrets" {
			continue
		}
		listed = true
		require.Equal(t, "type=kubernetes.io/tls",
			la.GetListRestrictions().Fields.String(),
			"the Secret watch must be narrowed server-side")
	}
	require.True(t, listed, "expected the watcher to list secrets")
}

func TestWatcherNamespaceScoping(t *testing.T) {
	// Restricting to named namespaces must exclude everything else, which is
	// what makes a namespace-scoped RBAC grant sufficient
	core := []runtime.Object{svc("shop", "web"), svc("other", "web")}
	w, c := start(t, opts(t, func(o *kubecfg.Options) {
		o.WatchNamespaces = []string{"shop"}
	}), core, nil)
	c.await(t, 1)

	got := w.Services()
	require.Len(t, got, 1)
	require.Equal(t, "shop", got[0].Namespace)
	require.NotNil(t, w.Service("shop", "web"))
	require.Nil(t, w.Service("other", "web"))
}

func TestWatcherNamespaceSelector(t *testing.T) {
	// A namespace selector filters results rather than the watch, because an
	// informer cannot select by the labels of a different object
	core := []runtime.Object{
		&corev1.Namespace{
			Name: "shop", Labels: map[string]string{"gateway": "yes"},
		},
		&corev1.Namespace{Name: "other"},
		svc("shop", "web"), svc("other", "web"),
	}
	w, c := start(t, opts(t, func(o *kubecfg.Options) {
		o.NamespaceSelector = map[string]string{"gateway": "yes"}
	}), core, nil)
	c.await(t, 1)

	got := w.Services()
	require.Len(t, got, 1)
	require.Equal(t, "shop", got[0].Namespace)
	require.True(t, w.NamespaceAllowed("shop"))
	require.False(t, w.NamespaceAllowed("other"))
	require.False(t, w.NamespaceAllowed("nonexistent"),
		"a namespace not in cache cannot be shown to match")
	require.Nil(t, w.Service("other", "web"))
}

func TestWatcherConstructionErrors(t *testing.T) {
	_, err := New(Config{Options: opts(t)})
	require.ErrorIs(t, err, ErrNoClient)

	_, err = New(Config{Client: kube.NewFromClientset(kubefake.NewClientset())})
	require.Error(t, err, "a watcher needs a validated configuration section")
}

func TestWatcherStopIsIdempotent(t *testing.T) {
	// Stop must be idempotent and must leave the shared factories alone for
	// any other consumer still holding them
	w, c := start(t, opts(t), []runtime.Object{svc("shop", "web")}, nil)
	c.await(t, 1)
	w.Stop()
	w.Stop()

	// a stopped watcher delivers nothing further
	before := c.n.Load()
	w.markDirty()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, before, c.n.Load())

	// starting a stopped watcher is a no-op rather than a panic
	require.NoError(t, w.Start(context.Background()))
}

func TestWatcherSurvivesHandlerPanic(t *testing.T) {
	// A panic in translation must not kill the watch layer and freeze the data
	// plane at its last-good configuration without saying so
	cs := kubefake.NewClientset()
	var calls atomic.Int32
	w, err := New(Config{
		Client:        kube.NewFromClientset(cs),
		GatewayClient: gwfake.NewSimpleClientset(),
		Options:       opts(t),
		OnChange: func() {
			if calls.Add(1) == 1 {
				panic("translation exploded")
			}
		},
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NotPanics(t, func() {
		require.NoError(t, w.Start(t.Context()))
	})

	_, err = cs.CoreV1().Services("shop").Create(context.Background(),
		svc("shop", "web"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return calls.Load() >= 2 },
		5*time.Second, 5*time.Millisecond,
		"the watch layer stopped delivering after a handler panic")
}

func TestWatcherWithoutHandler(t *testing.T) {
	// A watcher with no handler is legal: a caller may drive rebuilds itself
	w, err := New(Config{
		Client:        kube.NewFromClientset(kubefake.NewClientset()),
		GatewayClient: gwfake.NewSimpleClientset(),
		Options:       opts(t),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	require.NotPanics(t, w.markDirty)
}

func TestWatcherMultipleNamespaces(t *testing.T) {
	// Watching several named namespaces must keep their objects separate and
	// find each one in its own scope
	core := []runtime.Object{
		svc("shop", "web"), svc("infra", "api"), svc("other", "hidden"),
		tlsSecret("infra", "tls"),
	}
	w, c := start(t, opts(t, func(o *kubecfg.Options) {
		o.WatchNamespaces = []string{"shop", "infra"}
	}), core, nil)
	c.await(t, 1)

	got := w.Services()
	require.Len(t, got, 2)
	require.Equal(t, "infra", got[0].Namespace)
	require.Equal(t, "shop", got[1].Namespace)

	// a lookup must search the scope that holds the namespace, not just the
	// first one
	require.NotNil(t, w.Service("shop", "web"))
	require.NotNil(t, w.Service("infra", "api"))
	require.NotNil(t, w.Secret("infra", "tls"))
	require.Nil(t, w.Service("other", "hidden"))
	require.Nil(t, w.Secret("shop", "tls"))
}

func TestZeroWatcherAccessorsAreSafe(t *testing.T) {
	// The accessors are read by translators on every rebuild; a watcher that
	// failed to build must answer them rather than panic
	var w Watcher
	require.Nil(t, w.GatewayClasses())
	require.Nil(t, w.IngressClasses())
	require.Nil(t, w.Gateways())
	require.Nil(t, w.HTTPRoutes())
	require.Nil(t, w.ReferenceGrants())
	require.Nil(t, w.Ingresses())
	require.Nil(t, w.Services())
	require.Nil(t, w.Secrets())
	require.Nil(t, w.Service("ns", "name"))
	require.Nil(t, w.Secret("ns", "name"))
	require.True(t, w.NamespaceAllowed("anything"))
	require.False(t, w.HasSynced())

	// a selector with no namespace cache cannot show anything to match
	sel := Watcher{selector: labels.SelectorFromSet(labels.Set{"a": "b"})}
	require.False(t, sel.NamespaceAllowed("ns"))
}

func TestWatcherWithoutGatewayAPI(t *testing.T) {
	// Most clusters do not serve the Gateway API, and an informer over an unserved resource
	// never syncs, so a watcher without a gateway client builds none and serves everything else
	c := &changes{}
	w, err := New(Config{
		Client: kube.NewFromClientset(kubefake.NewClientset(
			svc("shop", "web"), ing("shop", "site"),
			&netv1.IngressClass{Name: "trickster"},
		)),
		Options:  opts(t),
		OnChange: c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.False(t, w.ServesGatewayAPI())
	require.NoError(t, w.Start(t.Context()))
	c.await(t, 1)

	require.Len(t, w.Services(), 1)
	require.Len(t, w.Ingresses(), 1)
	require.Len(t, w.IngressClasses(), 1)
	require.Empty(t, w.Gateways())
	require.Empty(t, w.HTTPRoutes())
	require.Empty(t, w.ReferenceGrants())
	require.Empty(t, w.GatewayClasses())
}

func TestWatcherReadsConfigMapsAndNamespacesForTheGatewayAPI(t *testing.T) {
	// ConfigMaps are read only for a GatewayClass's parameters, and Namespaces by a listener
	// admitting routes by label; both are held whenever the cluster serves the Gateway API
	core := []runtime.Object{
		&corev1.ConfigMap{
			Namespace: "infra", Name: "params", Data: map[string]string{"a": "b"},
		},
		&corev1.Namespace{
			Name: "tenant", Labels: map[string]string{"team": "a"},
		},
	}
	w, c := start(t, opts(t), core, nil)
	c.await(t, 1)
	require.NotNil(t, w.ConfigMap("infra", "params"))
	require.Nil(t, w.ConfigMap("infra", "absent"))
	require.NotNil(t, w.Namespace("tenant"))
	require.Equal(t, "a", w.Namespace("tenant").Labels["team"])
	require.Nil(t, w.Namespace("absent"))

	// scoped to another namespace, the ConfigMap is out of reach
	scoped, sc := start(t, opts(t, func(o *kubecfg.Options) {
		o.WatchNamespaces = []string{"shop"}
	}), core, nil)
	sc.await(t, 1)
	require.Nil(t, scoped.ConfigMap("infra", "params"))
}

func TestWatcherWithoutGatewayAPIHoldsNoConfigMapsOrNamespaces(t *testing.T) {
	// Without the Gateway API nothing reads ConfigMaps, and namespaces are held only for a
	// configured selector, so an Ingress-only controller needs neither grant
	cs := kubefake.NewClientset(
		&corev1.ConfigMap{Namespace: "infra", Name: "params"},
		&corev1.Namespace{Name: "tenant"},
	)
	c := &changes{}
	w, err := New(Config{
		Client: kube.NewFromClientset(cs), Options: opts(t), OnChange: c.handler(),
	})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	c.await(t, 1)
	require.Nil(t, w.ConfigMap("infra", "params"))
	require.Nil(t, w.Namespace("tenant"))
	for _, a := range cs.Actions() {
		r := a.GetResource().Resource
		require.NotEqual(t, "configmaps", r, "an Ingress-only controller must not list ConfigMaps")
		require.NotEqual(t, "namespaces", r, "nor namespaces without a selector")
	}

	var zero Watcher
	require.Nil(t, zero.ConfigMap("ns", "name"))
	require.Nil(t, zero.Namespace("name"))
}

func TestWatcherObjectGetters(t *testing.T) {
	// One object of every kind is reachable by name, and a kind the cluster does
	// not serve answers nothing rather than failing
	gc := &gwapiv1.GatewayClass{Name: "gc"}
	w, _ := start(t, opts(t), []runtime.Object{ing("shop", "web"), svc("shop", "s")},
		[]runtime.Object{httpRoute("shop", "web"), gc}, gw("shop", "gw"))
	require.NotNil(t, w.Ingress("shop", "web"))
	require.Nil(t, w.Ingress("shop", "nope"))
	require.NotNil(t, w.Gateway("shop", "gw"))
	require.Nil(t, w.Gateway("other", "gw"))
	require.NotNil(t, w.HTTPRoute("shop", "web"))
	require.Nil(t, w.HTTPRoute("shop", "nope"))
	require.NotNil(t, w.GatewayClass("gc"))
	require.Nil(t, w.GatewayClass("nope"))
	require.Nil(t, w.PublishedService(), "none is configured")

	plain, err := New(Config{
		Client:  kube.NewFromClientset(kubefake.NewClientset(ing("shop", "web"))),
		Options: opts(t),
	})
	require.NoError(t, err)
	t.Cleanup(plain.Stop)
	require.NoError(t, plain.Start(t.Context()))
	require.Nil(t, plain.Gateway("shop", "gw"))
	require.Nil(t, plain.HTTPRoute("shop", "web"))
	require.Nil(t, plain.GatewayClass("gc"))
	require.NotNil(t, plain.Ingress("shop", "web"))
}

func TestWatcherPublishedService(t *testing.T) {
	// The published Service is watched in its own namespace, outside the watched
	// ones, and a change to its addresses is a delivery like any other
	o := opts(t, func(o *kubecfg.Options) {
		o.WatchNamespaces = []string{"shop"}
		o.PublishedService = &kubecfg.ServiceRef{Namespace: "trickster", Name: "gateway"}
	})
	published := svc("trickster", "gateway")
	published.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "10.0.0.1"}}
	cs := kubefake.NewClientset(published, svc("trickster", "other"))
	c := &changes{}
	w, err := New(Config{Client: kube.NewFromClientset(cs), Options: o, OnChange: c.handler()})
	require.NoError(t, err)
	t.Cleanup(w.Stop)
	require.NoError(t, w.Start(t.Context()))
	c.await(t, 1)
	require.NotNil(t, w.PublishedService())
	require.Equal(t, "10.0.0.1", w.PublishedService().Status.LoadBalancer.Ingress[0].IP)
	require.Nil(t, w.Service("trickster", "gateway"), "the published namespace is not a watched one")

	published.Status.LoadBalancer.Ingress[0].IP = "10.0.0.2"
	_, err = cs.CoreV1().Services("trickster").UpdateStatus(t.Context(), published,
		metav1.UpdateOptions{})
	require.NoError(t, err)
	c.await(t, 2)
	require.Eventually(t, func() bool {
		return w.PublishedService().Status.LoadBalancer.Ingress[0].IP == "10.0.0.2"
	}, 5*time.Second, 5*time.Millisecond)
}

func TestObjectKey(t *testing.T) {
	require.Equal(t, "shop/web", ObjectKey(ing("shop", "web")))
	require.Equal(t, "gc", ObjectKey(&gwapiv1.GatewayClass{Name: "gc"}))
	require.Equal(t, "a/b", ObjectKey(cache.DeletedFinalStateUnknown{Key: "a/b"}))
	require.Equal(t, "", ObjectKey("not an object"))
}

func TestWatchEventsAreCounted(t *testing.T) {
	before := testutil.ToFloat64(metrics.KubeWatchEvents.WithLabelValues(ir.KindIngress, eventAdd))
	_, c := start(t, opts(t), []runtime.Object{ing("shop", "web")}, nil)
	c.await(t, 1)
	require.GreaterOrEqual(t,
		testutil.ToFloat64(metrics.KubeWatchEvents.WithLabelValues(ir.KindIngress, eventAdd)),
		before+1)
}
