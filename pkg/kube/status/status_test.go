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

package status

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

const controllerName = appinfo.Domain + "/gateway-controller"

// gatewayGVR is the Gateway resource, named explicitly because the fake's
// tracker pluralizes the kind to "gatewaies"
var gatewayGVR = schema.GroupVersionResource{
	Group: gwapiv1.GroupName, Version: "v1", Resource: "gateways",
}

// cache is a Cache over fixed objects, which a test updates with what was written
type cache struct {
	classes   map[string]*gwapiv1.GatewayClass
	gateways  map[string]*gwapiv1.Gateway
	routes    map[string]*gwapiv1.HTTPRoute
	grpc      map[string]*gwapiv1.GRPCRoute
	tcp       map[string]*gwapiv1a2.TCPRoute
	tlsRoutes map[string]*gwapiv1a2.TLSRoute
	udp       map[string]*gwapiv1a2.UDPRoute
	ingresses map[string]*netv1.Ingress
	policies  map[string]*cachepolicy.CachePolicy
}

func newCache() *cache {
	return &cache{
		classes:   make(map[string]*gwapiv1.GatewayClass),
		gateways:  make(map[string]*gwapiv1.Gateway),
		routes:    make(map[string]*gwapiv1.HTTPRoute),
		grpc:      make(map[string]*gwapiv1.GRPCRoute),
		tcp:       make(map[string]*gwapiv1a2.TCPRoute),
		tlsRoutes: make(map[string]*gwapiv1a2.TLSRoute),
		udp:       make(map[string]*gwapiv1a2.UDPRoute),
		ingresses: make(map[string]*netv1.Ingress),
		policies:  make(map[string]*cachepolicy.CachePolicy),
	}
}

func (c *cache) GatewayClass(name string) *gwapiv1.GatewayClass { return c.classes[name] }
func (c *cache) Gateway(ns, name string) *gwapiv1.Gateway       { return c.gateways[ns+"/"+name] }
func (c *cache) HTTPRoute(ns, name string) *gwapiv1.HTTPRoute   { return c.routes[ns+"/"+name] }
func (c *cache) GRPCRoute(ns, name string) *gwapiv1.GRPCRoute   { return c.grpc[ns+"/"+name] }
func (c *cache) TCPRoute(ns, name string) *gwapiv1a2.TCPRoute   { return c.tcp[ns+"/"+name] }
func (c *cache) TLSRoute(ns, name string) *gwapiv1a2.TLSRoute   { return c.tlsRoutes[ns+"/"+name] }
func (c *cache) UDPRoute(ns, name string) *gwapiv1a2.UDPRoute   { return c.udp[ns+"/"+name] }
func (c *cache) Ingress(ns, name string) *netv1.Ingress         { return c.ingresses[ns+"/"+name] }
func (c *cache) CachePolicy(ns, name string) *cachepolicy.CachePolicy {
	return c.policies[ns+"/"+name]
}

// policyListKinds names the list kind of the resource for the fake dynamic client
var policyListKinds = map[schema.GroupVersionResource]string{
	cachepolicy.GVR: cachepolicy.Kind + "List",
}

func cachePolicy() *cachepolicy.CachePolicy {
	return &cachepolicy.CachePolicy{
		Namespace: "shop", Name: "cp", Generation: 5, UID: "cp-uid",
		Spec: cachepolicy.Spec{TargetRefs: []cachepolicy.TargetRef{
			{Kind: cachepolicy.KindHTTPRoute, Name: "web"},
		}},
		Status: gwapiv1.PolicyStatus{Ancestors: []gwapiv1.PolicyAncestorStatus{{
			AncestorRef: gwapiv1.ParentReference{Name: "web"}, ControllerName: "other.example.com",
		}}},
	}
}

func policyReport() ir.PolicyStatus {
	return ir.PolicyStatus{
		Source: ir.Source{
			Kind: ir.KindCachePolicy, Namespace: "shop", Name: "cp",
			Generation: 5, UID: "cp-uid",
		},
		Ancestors: []ir.AncestorStatus{{
			Ref:        ir.ParentRef{Kind: cachepolicy.KindHTTPRoute, Namespace: "shop", Name: "web"},
			Conditions: []ir.Condition{cond("Accepted", true, "Accepted")},
		}},
	}
}

func class() *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		Name: "trickster", Generation: 2,
		Spec: gwapiv1.GatewayClassSpec{ControllerName: controllerName},
	}
}

func gateway() *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		Namespace: "infra", Name: "gw", Generation: 3,
		Spec: gwapiv1.GatewaySpec{GatewayClassName: "trickster", Listeners: []gwapiv1.Listener{
			{Name: "http", Port: 80, Protocol: gwapiv1.HTTPProtocolType},
		}},
	}
}

func route() *gwapiv1.HTTPRoute {
	ns := gwapiv1.Namespace("infra")
	return &gwapiv1.HTTPRoute{
		Namespace: "shop", Name: "web", Generation: 4,
		Spec: gwapiv1.HTTPRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Namespace: &ns, Name: "gw"}},
		}},
	}
}

func ingress() *netv1.Ingress {
	return &netv1.Ingress{Namespace: "shop", Name: "web"}
}

func cond(typ string, status bool, reason string) ir.Condition {
	return ir.Condition{Type: typ, Status: status, Reason: reason, Message: reason + " message"}
}

func report() *ir.Report {
	gwSrc := ir.Source{Kind: ir.KindGateway, Namespace: "infra", Name: "gw", Generation: 3}
	return &ir.Report{
		Classes: []ir.ClassStatus{{
			Source:   ir.Source{Kind: ir.KindGatewayClass, Name: "trickster", Generation: 2},
			Accepted: cond("Accepted", true, "Accepted"),
		}},
		Gateways: []ir.GatewayStatus{{
			Source: gwSrc,
			Conditions: []ir.Condition{
				cond("Accepted", true, "Accepted"), cond("Programmed", true, "Programmed"),
			},
			Listeners: []ir.ListenerStatus{{
				Name: "http", SupportedKinds: []string{"HTTPRoute"}, AttachedRoutes: 1,
				Conditions: []ir.Condition{
					cond("Accepted", true, "Accepted"), cond("Programmed", true, "Programmed"),
					cond("ResolvedRefs", true, "ResolvedRefs"),
					cond("Conflicted", false, "NoConflicts"),
				},
			}},
		}},
		Routes: []ir.RouteStatus{{
			Source: ir.Source{Kind: ir.KindHTTPRoute, Namespace: "shop", Name: "web", Generation: 4},
			Parents: []ir.ParentStatus{{
				Ref: ir.ParentRef{Namespace: "infra", Name: "gw"},
				Conditions: []ir.Condition{
					cond("Accepted", true, "Accepted"), cond("ResolvedRefs", true, "ResolvedRefs"),
				},
			}},
		}},
		Ingresses: []ir.Source{{Kind: ir.KindIngress, Namespace: "shop", Name: "web"}},
	}
}

// harness is a writer over fake clientsets holding the fixtures
type harness struct {
	cs   *kubefake.Clientset
	gwcs *gwfake.Clientset
	dyn  *dynamicfake.FakeDynamicClient
	c    *cache
	w    *Writer
}

func newHarness(t *testing.T, objects ...runtime.Object) *harness {
	t.Helper()
	h := &harness{c: newCache()}
	h.cs = kubefake.NewClientset(ingress())
	h.gwcs = gwfake.NewSimpleClientset(append([]runtime.Object{class(), route()}, objects...)...)
	gw := gateway()
	require.NoError(t, h.gwcs.Tracker().Create(gatewayGVR, gw, gw.Namespace))
	cp, err := cachepolicy.ToUnstructured(cachePolicy())
	require.NoError(t, err)
	h.dyn = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		policyListKinds, cp)
	h.c.classes["trickster"] = class()
	h.c.gateways["infra/gw"] = gw
	h.c.routes["shop/web"] = route()
	h.c.ingresses["shop/web"] = ingress()
	h.c.policies["shop/cp"] = cachePolicy()
	h.w = New(Config{
		Client: h.cs, GatewayClient: h.gwcs, DynamicClient: h.dyn, Cache: h.c,
		ControllerName: controllerName, Timeout: time.Second,
	})
	return h
}

func (h *harness) refresh(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	u, err := h.dyn.Resource(cachepolicy.GVR).Namespace("shop").Get(ctx, "cp", metav1.GetOptions{})
	require.NoError(t, err)
	cp, err := cachepolicy.FromUnstructured(u)
	require.NoError(t, err)
	h.c.policies["shop/cp"] = cp
	gc, err := h.gwcs.GatewayV1().GatewayClasses().Get(ctx, "trickster", metav1.GetOptions{})
	require.NoError(t, err)
	h.c.classes["trickster"] = gc
	gw, err := h.gwcs.GatewayV1().Gateways("infra").Get(ctx, "gw", metav1.GetOptions{})
	require.NoError(t, err)
	h.c.gateways["infra/gw"] = gw
	r, err := h.gwcs.GatewayV1().HTTPRoutes("shop").Get(ctx, "web", metav1.GetOptions{})
	require.NoError(t, err)
	h.c.routes["shop/web"] = r
	i, err := h.cs.NetworkingV1().Ingresses("shop").Get(ctx, "web", metav1.GetOptions{})
	require.NoError(t, err)
	h.c.ingresses["shop/web"] = i
}

func updates(actions []ktesting.Action) int {
	var n int
	for _, a := range actions {
		if a.GetVerb() == "update" {
			n++
		}
	}
	return n
}

func updatesOf(actions []ktesting.Action, resource string) int {
	var n int
	for _, a := range actions {
		if a.GetVerb() == "update" && a.GetResource().Resource == resource {
			n++
		}
	}
	return n
}

func TestWriteEverything(t *testing.T) {
	h := newHarness(t)
	in := Input{
		Report: report(), Programmed: true, WantAddresses: true,
		Addresses: []Address{
			{Type: AddressIP, Value: "10.0.0.1"},
			{Type: AddressHostname, Value: "lb.example.com"},
		},
	}
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	h.refresh(t)

	gc := h.c.classes["trickster"]
	accepted := meta.FindStatusCondition(gc.Status.Conditions, "Accepted")
	require.NotNil(t, accepted)
	require.Equal(t, metav1.ConditionTrue, accepted.Status)
	require.EqualValues(t, 2, accepted.ObservedGeneration)

	gw := h.c.gateways["infra/gw"]
	require.Equal(t, metav1.ConditionTrue,
		meta.FindStatusCondition(gw.Status.Conditions, "Programmed").Status)
	require.EqualValues(t, 3, meta.FindStatusCondition(gw.Status.Conditions, "Accepted").
		ObservedGeneration)
	require.Len(t, gw.Status.Addresses, 2)
	require.Equal(t, gwapiv1.IPAddressType, *gw.Status.Addresses[0].Type)
	require.Equal(t, "lb.example.com", gw.Status.Addresses[1].Value)
	require.Len(t, gw.Status.Listeners, 1)
	l := gw.Status.Listeners[0]
	require.EqualValues(t, "http", l.Name)
	require.EqualValues(t, 1, l.AttachedRoutes)
	require.Len(t, l.SupportedKinds, 1)
	require.EqualValues(t, gwapiv1.GroupName, *l.SupportedKinds[0].Group)
	require.EqualValues(t, "HTTPRoute", l.SupportedKinds[0].Kind)
	require.Len(t, l.Conditions, 4)
	require.Equal(t, metav1.ConditionFalse,
		meta.FindStatusCondition(l.Conditions, "Conflicted").Status)

	r := h.c.routes["shop/web"]
	require.Len(t, r.Status.Parents, 1)
	p := r.Status.Parents[0]
	require.EqualValues(t, controllerName, p.ControllerName)
	require.EqualValues(t, "infra", *p.ParentRef.Namespace)
	require.Nil(t, p.ParentRef.Group)
	require.Len(t, p.Conditions, 2)
	require.EqualValues(t, 4, p.Conditions[0].ObservedGeneration)

	i := h.c.ingresses["shop/web"]
	require.Equal(t, []netv1.IngressLoadBalancerIngress{
		{IP: "10.0.0.1"}, {Hostname: "lb.example.com"},
	}, i.Status.LoadBalancer.Ingress)

	// the same input against what is now in cache writes nothing
	h.cs.ClearActions()
	h.gwcs.ClearActions()
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	require.Equal(t, 0, updates(h.cs.Actions()))
	require.Equal(t, 0, updates(h.gwcs.Actions()))

	// a changed message is a write that keeps the transition time
	before := meta.FindStatusCondition(gw.Status.Conditions, "Accepted").LastTransitionTime
	in.Report.Gateways[0].Conditions[0].Message = "still served"
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	h.refresh(t)
	after := meta.FindStatusCondition(h.c.gateways["infra/gw"].Status.Conditions, "Accepted")
	require.Equal(t, "still served", after.Message)
	require.Equal(t, before, after.LastTransitionTime)
}

func TestWritePolicies(t *testing.T) {
	// a policy's ancestors are written per targetRef under this controller's name, another
	// controller's entries are kept, and an unchanged status is not written again
	h := newHarness(t)
	in := Input{Report: &ir.Report{Policies: []ir.PolicyStatus{policyReport()}}, Programmed: true}
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	h.refresh(t)
	cp := h.c.policies["shop/cp"]
	require.Len(t, cp.Status.Ancestors, 2)
	require.EqualValues(t, "other.example.com", cp.Status.Ancestors[0].ControllerName)
	ours := cp.Status.Ancestors[1]
	require.EqualValues(t, controllerName, ours.ControllerName)
	require.EqualValues(t, cachepolicy.KindHTTPRoute, *ours.AncestorRef.Kind)
	require.EqualValues(t, "shop", *ours.AncestorRef.Namespace)
	require.EqualValues(t, "web", ours.AncestorRef.Name)
	require.Len(t, ours.Conditions, 1)
	require.Equal(t, metav1.ConditionTrue, ours.Conditions[0].Status)
	require.EqualValues(t, 5, ours.Conditions[0].ObservedGeneration)

	h.dyn.ClearActions()
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	require.Equal(t, 0, updates(h.dyn.Actions()))

	// a changed verdict keeps the transition time of a condition whose status held
	before := ours.Conditions[0].LastTransitionTime
	in.Report.Policies[0].Ancestors[0].Conditions[0].Message = "still governs"
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	h.refresh(t)
	after := h.c.policies["shop/cp"].Status.Ancestors[1].Conditions[0]
	require.Equal(t, "still governs", after.Message)
	require.Equal(t, before, after.LastTransitionTime)

	// a policy the cache no longer holds, or one translated at another generation, is skipped
	h.c.policies["shop/cp"].Generation = 6
	h.dyn.ClearActions()
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	require.Equal(t, 0, updates(h.dyn.Actions()))
	delete(h.c.policies, "shop/cp")
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	require.Equal(t, 0, updates(h.dyn.Actions()))

	// without the resource nothing is written for it
	h.c.policies["shop/cp"] = cachePolicy()
	none := New(Config{
		Client: h.cs, GatewayClient: h.gwcs, Cache: h.c,
		ControllerName: controllerName,
	})
	require.Equal(t, 0, none.Write(t.Context(), in))
	require.Equal(t, 0, updates(h.dyn.Actions()))
}

func TestWritePoliciesRetriesOnConflict(t *testing.T) {
	// a conflict re-reads the policy and applies the same verdict again; a term that has ended
	// writes nothing at all
	h := newHarness(t)
	var conflicts int
	h.dyn.PrependReactor("update", cachepolicy.Resource,
		func(ktesting.Action) (bool, runtime.Object, error) {
			if conflicts > 0 {
				return false, nil, nil
			}
			conflicts++
			return true, nil, apierrors.NewConflict(cachepolicy.GVR.GroupResource(), "cp",
				errors.New("modified"))
		})
	in := Input{Report: &ir.Report{Policies: []ir.PolicyStatus{policyReport()}}}
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	require.Equal(t, 1, conflicts)
	h.refresh(t)
	require.Len(t, h.c.policies["shop/cp"].Status.Ancestors, 2)

	ended, cancel := context.WithCancel(t.Context())
	cancel()
	h.dyn.ClearActions()
	require.Equal(t, 0, h.w.Write(ended, in))
	require.Equal(t, 0, updates(h.dyn.Actions()))
}

func TestWritePoliciesCountsFailures(t *testing.T) {
	h := newHarness(t)
	h.dyn.PrependReactor("update", cachepolicy.Resource,
		func(ktesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("refused")
		})
	in := Input{Report: &ir.Report{Policies: []ir.PolicyStatus{policyReport()}}}
	require.Equal(t, 1, h.w.Write(t.Context(), in))
}

func TestWriteLowersProgrammed(t *testing.T) {
	h := newHarness(t)
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: report(), Programmed: false}))
	h.refresh(t)
	gw := h.c.gateways["infra/gw"]
	programmed := meta.FindStatusCondition(gw.Status.Conditions, "Programmed")
	require.Equal(t, metav1.ConditionFalse, programmed.Status)
	require.EqualValues(t, gwapiv1.GatewayReasonPending, programmed.Reason)
	lp := meta.FindStatusCondition(gw.Status.Listeners[0].Conditions, "Programmed")
	require.Equal(t, metav1.ConditionFalse, lp.Status)
	require.Nil(t, gw.Status.Addresses)
	require.Equal(t, metav1.ConditionTrue,
		meta.FindStatusCondition(gw.Status.Conditions, "Accepted").Status)

	// a published service with no address yet is a gateway-level wait, not a listener one
	require.Equal(t, 0, h.w.Write(t.Context(), Input{
		Report: report(), Programmed: true,
		WantAddresses: true,
	}))
	h.refresh(t)
	gw = h.c.gateways["infra/gw"]
	programmed = meta.FindStatusCondition(gw.Status.Conditions, "Programmed")
	require.EqualValues(t, gwapiv1.GatewayReasonAddressNotAssigned, programmed.Reason)
	require.Equal(t, metav1.ConditionTrue,
		meta.FindStatusCondition(gw.Status.Listeners[0].Conditions, "Programmed").Status)
}

func TestWriteKeepsOtherControllersRouteParents(t *testing.T) {
	h := newHarness(t)
	ns := gwapiv1.Namespace("infra")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	r := route()
	r.Status.Parents = []gwapiv1.RouteParentStatus{
		{
			ParentRef: gwapiv1.ParentReference{Name: "theirs"}, ControllerName: "other.io/ctl",
			Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue}},
		},
		{
			ParentRef: gwapiv1.ParentReference{Name: "stale"}, ControllerName: controllerName,
			Conditions: []metav1.Condition{{Type: "Accepted", Status: metav1.ConditionTrue}},
		},
		{
			ParentRef:      gwapiv1.ParentReference{Namespace: &ns, Name: "gw"},
			ControllerName: controllerName,
			Conditions: []metav1.Condition{{
				Type: "Accepted", Status: metav1.ConditionTrue,
				Reason: "Accepted", LastTransitionTime: metav1.NewTime(old),
			}},
		},
	}
	h.c.routes["shop/web"] = r
	_, err := h.gwcs.GatewayV1().HTTPRoutes("shop").Update(t.Context(), r, metav1.UpdateOptions{})
	require.NoError(t, err)

	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
	h.refresh(t)
	parents := h.c.routes["shop/web"].Status.Parents
	require.Len(t, parents, 2)
	require.EqualValues(t, "other.io/ctl", parents[0].ControllerName)
	require.EqualValues(t, "gw", parents[1].ParentRef.Name)
	accepted := meta.FindStatusCondition(parents[1].Conditions, "Accepted")
	require.Equal(t, old, accepted.LastTransitionTime.Time, "an unchanged status keeps its time")
	resolved := meta.FindStatusCondition(parents[1].Conditions, "ResolvedRefs")
	require.NotNil(t, resolved)

	// flipping the status moves the transition time
	rep := report()
	rep.Routes[0].Parents[0].Conditions[0] = cond("Accepted", false, "NoMatchingParent")
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: rep, Programmed: true}))
	h.refresh(t)
	accepted = meta.FindStatusCondition(h.c.routes["shop/web"].Status.Parents[1].Conditions,
		"Accepted")
	require.Equal(t, metav1.ConditionFalse, accepted.Status)
	require.NotEqual(t, old, accepted.LastTransitionTime.Time)
}

func TestWriteRetriesOnConflict(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	h.gwcs.PrependReactor("update", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		if calls.Add(1) == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Resource: "gateways"}, "gw", errors.New("stale"))
		}
		return false, nil, nil
	})
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
	require.EqualValues(t, 2, calls.Load())
	h.refresh(t)
	require.NotEmpty(t, h.c.gateways["infra/gw"].Status.Conditions)
}

func TestWriteCountsFailures(t *testing.T) {
	h := newHarness(t)
	h.gwcs.PrependReactor("update", "httproutes", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("forbidden")
	})
	before := testutil.ToFloat64(metrics.KubeStatusWriteFailures.WithLabelValues(ir.KindHTTPRoute))
	require.Equal(t, 1, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
	require.Equal(t, before+1,
		testutil.ToFloat64(metrics.KubeStatusWriteFailures.WithLabelValues(ir.KindHTTPRoute)))

	// a conflict that never resolves is a failure too, after the retries
	h.gwcs.PrependReactor("update", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "gateways"}, "gw", errors.New("stale"))
	})
	require.Equal(t, 2, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
}

func TestWriteSkipsWhatIsNotThere(t *testing.T) {
	h := newHarness(t)
	h.c = newCache()
	h.w = New(Config{Client: h.cs, GatewayClient: h.gwcs, Cache: h.c, ControllerName: controllerName})
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
	require.Equal(t, 0, updates(h.gwcs.Actions()))
	require.Equal(t, 0, updates(h.cs.Actions()))

	var nilWriter *Writer
	require.Equal(t, 0, nilWriter.Write(t.Context(), Input{Report: report()}))
	require.Equal(t, 0, h.w.Write(t.Context(), Input{}))
}

func TestWriteWithoutGatewayAPI(t *testing.T) {
	h := newHarness(t)
	h.w = New(Config{Client: h.cs, Cache: h.c, ControllerName: controllerName})
	require.Equal(t, 0, h.w.Write(t.Context(), Input{
		Report: report(), Programmed: true,
		WantAddresses: true, Addresses: []Address{{Type: AddressIP, Value: "10.0.0.2"}},
	}))
	require.Equal(t, 0, updates(h.gwcs.Actions()))
	h.refresh(t)
	require.Equal(t, "10.0.0.2", h.c.ingresses["shop/web"].Status.LoadBalancer.Ingress[0].IP)
	require.Empty(t, h.c.gateways["infra/gw"].Status.Conditions)
}

func TestAddresses(t *testing.T) {
	require.Nil(t, Addresses(nil))
	svc := &corev1.Service{Spec: corev1.ServiceSpec{ExternalIPs: []string{"192.0.2.1"}}}
	require.Equal(t, []Address{{Type: AddressIP, Value: "192.0.2.1"}}, Addresses(svc))
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{
		{IP: "10.0.0.1"}, {Hostname: "lb.example.com"},
	}
	require.Equal(t, []Address{
		{Type: AddressIP, Value: "10.0.0.1"}, {Type: AddressHostname, Value: "lb.example.com"},
	}, Addresses(svc), "load balancer addresses take precedence over external IPs")
	require.Nil(t, Addresses(&corev1.Service{}))
}

func TestParentRefRoundTrip(t *testing.T) {
	in := ir.ParentRef{
		Group: gwapiv1.GroupName, Kind: "Gateway", Namespace: "infra",
		Name: "gw", SectionName: "http", Port: 80,
	}
	ref := parentRef(in)
	require.EqualValues(t, gwapiv1.GroupName, *ref.Group)
	require.EqualValues(t, "Gateway", *ref.Kind)
	require.EqualValues(t, "http", *ref.SectionName)
	require.EqualValues(t, 80, *ref.Port)
	require.Equal(t, "gateway.networking.k8s.io|Gateway|infra|gw|http|80", parentKey(ref))
	require.Equal(t, "|||gw||", parentKey(parentRef(ir.ParentRef{Name: "gw"})))
}

func TestWriteSkipsSupersededObjects(t *testing.T) {
	h := newHarness(t)
	edited := gateway()
	edited.Generation = 4
	h.c.gateways["infra/gw"] = edited
	replaced := route()
	replaced.UID = "new"
	h.c.routes["shop/web"] = replaced
	rep := report()
	rep.Routes[0].Source.UID = "old"
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: rep, Programmed: true}))
	require.Equal(t, 0, updatesOf(h.gwcs.Actions(), "gateways"), "an edited object is not written")
	require.Equal(t, 0, updatesOf(h.gwcs.Actions(), "httproutes"), "a replaced object is not written")
	require.Equal(t, 1, updatesOf(h.gwcs.Actions(), "gatewayclasses"),
		"the object the verdict is about is written")

	// the object changes between the conflict and the re-read: the re-read
	// verdict is dropped rather than relabeled with the new generation
	h = newHarness(t)
	var calls atomic.Int32
	h.gwcs.PrependReactor("update", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Resource: "gateways"}, "gw", errors.New("stale"))
	})
	h.gwcs.PrependReactor("get", "gateways", func(ktesting.Action) (bool, runtime.Object, error) {
		fresh := gateway()
		fresh.Generation = 4
		return true, fresh, nil
	})
	before := testutil.ToFloat64(metrics.KubeStatusWriteFailures.WithLabelValues(ir.KindGateway))
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
	require.EqualValues(t, 1, calls.Load(), "no second write against the newer generation")
	require.Equal(t, before, testutil.ToFloat64(
		metrics.KubeStatusWriteFailures.WithLabelValues(ir.KindGateway)))
}

func TestWriteIngressAddressesOnlyWhenPublishing(t *testing.T) {
	h := newHarness(t)
	existing := ingress()
	existing.Status.LoadBalancer.Ingress = []netv1.IngressLoadBalancerIngress{{IP: "203.0.113.9"}}
	h.c.ingresses["shop/web"] = existing
	_, err := h.cs.NetworkingV1().Ingresses("shop").UpdateStatus(t.Context(), existing,
		metav1.UpdateOptions{})
	require.NoError(t, err)
	h.cs.ClearActions()
	require.Equal(t, 0, h.w.Write(t.Context(), Input{Report: report(), Programmed: true}))
	require.Equal(t, 0, updates(h.cs.Actions()))
	h.refresh(t)
	require.Equal(t, "203.0.113.9", h.c.ingresses["shop/web"].Status.LoadBalancer.Ingress[0].IP)

	// a configured Service that has lost its addresses clears what this controller published
	require.Equal(t, 0, h.w.Write(t.Context(), Input{
		Report: report(), Programmed: true,
		WantAddresses: true,
	}))
	h.refresh(t)
	require.Empty(t, h.c.ingresses["shop/web"].Status.LoadBalancer.Ingress)
}

func TestWriteStopsWhenTheTermEnds(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.Equal(t, 0, h.w.Write(ctx, Input{Report: report(), Programmed: true}))
	require.Equal(t, 0, updates(h.gwcs.Actions()))
	require.Equal(t, 0, updates(h.cs.Actions()))

	h = newHarness(t)
	ctx, cancel = context.WithCancel(t.Context())
	t.Cleanup(cancel)
	before := testutil.ToFloat64(metrics.KubeStatusWriteFailures.WithLabelValues(ir.KindGatewayClass))
	h.gwcs.PrependReactor("update", "gatewayclasses", func(ktesting.Action) (bool, runtime.Object, error) {
		// the term ends while the first write is in flight
		cancel()
		return true, nil, context.Canceled
	})
	require.Equal(t, 0, h.w.Write(ctx, Input{Report: report(), Programmed: true}))
	require.Equal(t, 1, updates(h.gwcs.Actions()), "nothing after the write the term ended on")
	require.Equal(t, before,
		testutil.ToFloat64(metrics.KubeStatusWriteFailures.WithLabelValues(ir.KindGatewayClass)))
}

func TestWriteStreamRouteStatus(t *testing.T) {
	// the experimental route kinds carry the same status, written through their own clients
	ns := gwapiv1.Namespace("infra")
	common := gwapiv1.CommonRouteSpec{ParentRefs: []gwapiv1.ParentReference{{Namespace: &ns, Name: "gw"}}}
	tcp := &gwapiv1a2.TCPRoute{
		Namespace: "data", Name: "db", Generation: 2,
		Spec: gwapiv1a2.TCPRouteSpec{CommonRouteSpec: common},
	}
	tlsr := &gwapiv1a2.TLSRoute{
		Namespace: "data", Name: "shop", Generation: 3,
		Spec: gwapiv1a2.TLSRouteSpec{CommonRouteSpec: common},
	}
	udp := &gwapiv1a2.UDPRoute{
		Namespace: "data", Name: "dns", Generation: 4,
		Spec: gwapiv1a2.UDPRouteSpec{CommonRouteSpec: common},
	}
	h := newHarness(t, tcp, tlsr, udp)
	h.c.tcp["data/db"] = tcp
	h.c.tlsRoutes["data/shop"] = tlsr
	h.c.udp["data/dns"] = udp
	parents := []ir.ParentStatus{{
		Ref:        ir.ParentRef{Namespace: "infra", Name: "gw"},
		Conditions: []ir.Condition{cond("Accepted", true, "Accepted")},
	}}
	in := Input{Report: &ir.Report{Routes: []ir.RouteStatus{
		{Source: ir.Source{Kind: ir.KindTCPRoute, Namespace: "data", Name: "db", Generation: 2}, Parents: parents},
		{Source: ir.Source{Kind: ir.KindTLSRoute, Namespace: "data", Name: "shop", Generation: 3}, Parents: parents},
		{Source: ir.Source{Kind: ir.KindUDPRoute, Namespace: "data", Name: "dns", Generation: 4}, Parents: parents},
		{Source: ir.Source{Kind: ir.KindTCPRoute, Namespace: "data", Name: "gone"}, Parents: parents},
	}}}
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	alpha := h.gwcs.GatewayV1alpha2()
	gotTCP, err := alpha.TCPRoutes("data").Get(t.Context(), "db", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, gotTCP.Status.Parents, 1)
	require.EqualValues(t, controllerName, gotTCP.Status.Parents[0].ControllerName)
	require.EqualValues(t, 2, gotTCP.Status.Parents[0].Conditions[0].ObservedGeneration)
	gotTLS, err := alpha.TLSRoutes("data").Get(t.Context(), "shop", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, gotTLS.Status.Parents, 1)
	gotUDP, err := alpha.UDPRoutes("data").Get(t.Context(), "dns", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, gotUDP.Status.Parents, 1)
	require.EqualValues(t, 4, gotUDP.Status.Parents[0].Conditions[0].ObservedGeneration)
}

func TestWriteGRPCRouteStatus(t *testing.T) {
	// A GRPCRoute's status is written as an HTTPRoute's is, under this controller's name, and
	// one the cache no longer holds is skipped rather than failed
	ns := gwapiv1.Namespace("infra")
	gr := &gwapiv1.GRPCRoute{
		Namespace: "shop", Name: "rpc", Generation: 2,
		Spec: gwapiv1.GRPCRouteSpec{CommonRouteSpec: gwapiv1.CommonRouteSpec{
			ParentRefs: []gwapiv1.ParentReference{{Namespace: &ns, Name: "gw"}},
		}},
	}
	h := newHarness(t, gr)
	h.c.grpc["shop/rpc"] = gr
	src := ir.Source{Kind: ir.KindGRPCRoute, Namespace: "shop", Name: "rpc", Generation: 2}
	in := Input{Report: &ir.Report{Routes: []ir.RouteStatus{{
		Source: src,
		Parents: []ir.ParentStatus{{
			Ref:        ir.ParentRef{Namespace: "infra", Name: "gw"},
			Conditions: []ir.Condition{cond("Accepted", true, "Accepted")},
		}},
	}, {
		Source: ir.Source{Kind: ir.KindGRPCRoute, Namespace: "shop", Name: "gone"},
		Parents: []ir.ParentStatus{{
			Ref:        ir.ParentRef{Namespace: "infra", Name: "gw"},
			Conditions: []ir.Condition{cond("Accepted", true, "Accepted")},
		}},
	}}}}
	require.Equal(t, 0, h.w.Write(t.Context(), in))
	got, err := h.gwcs.GatewayV1().GRPCRoutes("shop").Get(t.Context(), "rpc", metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, got.Status.Parents, 1)
	require.EqualValues(t, controllerName, got.Status.Parents[0].ControllerName)
	require.EqualValues(t, "infra", *got.Status.Parents[0].ParentRef.Namespace)
	require.Len(t, got.Status.Parents[0].Conditions, 1)
	require.EqualValues(t, 2, got.Status.Parents[0].Conditions[0].ObservedGeneration)
	require.Equal(t, 1, updatesOf(h.gwcs.Actions(), "grpcroutes"))
}
