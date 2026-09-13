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

// Package status writes a translation pass's verdicts back onto the objects it read, as a
// read-modify-write that keeps what others wrote and skips an object that already agrees
package status

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/kube"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwclient "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
)

// maxAttempts bounds the conflict retries of one status write; a conflict means another writer
// changed the object, and the next reconcile pass re-reads it anyway
const maxAttempts = 3

// Address types, as the Gateway API names them
const (
	AddressIP       = string(gwapiv1.IPAddressType)
	AddressHostname = string(gwapiv1.HostnameAddressType)
)

// Address is one published address
type Address struct {
	Type  string
	Value string
}

// Addresses reads the addresses a Service has been assigned: those a load balancer reported, or
// failing that its external IPs. A Service with neither publishes nothing.
func Addresses(svc *corev1.Service) []Address {
	if svc == nil {
		return nil
	}
	var out []Address
	for _, in := range svc.Status.LoadBalancer.Ingress {
		if in.IP != "" {
			out = append(out, Address{Type: AddressIP, Value: in.IP})
		}
		if in.Hostname != "" {
			out = append(out, Address{Type: AddressHostname, Value: in.Hostname})
		}
	}
	if len(out) == 0 {
		for _, ip := range svc.Spec.ExternalIPs {
			out = append(out, Address{Type: AddressIP, Value: ip})
		}
	}
	return out
}

// Cache reads the current objects, so a status that already says what this
// pass would say costs no API call
type Cache interface {
	GatewayClass(name string) *gwapiv1.GatewayClass
	Gateway(namespace, name string) *gwapiv1.Gateway
	HTTPRoute(namespace, name string) *gwapiv1.HTTPRoute
	GRPCRoute(namespace, name string) *gwapiv1.GRPCRoute
	TCPRoute(namespace, name string) *gwapiv1a2.TCPRoute
	TLSRoute(namespace, name string) *gwapiv1a2.TLSRoute
	UDPRoute(namespace, name string) *gwapiv1a2.UDPRoute
	Ingress(namespace, name string) *netv1.Ingress
	CachePolicy(namespace, name string) *cachepolicy.CachePolicy
}

// Config carries the writer's dependencies
type Config struct {
	// Client writes Ingress status
	Client kubernetes.Interface
	// GatewayClient writes Gateway API status; nil in a cluster without the Gateway API
	GatewayClient gwclient.Interface
	// DynamicClient writes cache policy status; nil in a cluster without the resource
	DynamicClient dynamic.Interface
	// Cache reads the objects as the controller last saw them
	Cache Cache
	// ControllerName is what route parent entries are attributed to
	ControllerName string
	// Timeout bounds each API call; zero applies none
	Timeout time.Duration
}

// Writer writes status for one controller configuration
type Writer struct {
	cfg Config
}

// New returns a Writer over the configuration
func New(cfg Config) *Writer {
	return &Writer{cfg: cfg}
}

// Input is what one pass writes
type Input struct {
	// Report is the translation pass's verdict on every claimed object
	Report *ir.Report
	// Programmed is false when the data plane rejected the generated configuration, in which case
	// nothing in the report is programmed whatever the translator concluded
	Programmed bool
	// Addresses are the published Service's, and WantAddresses whether one is configured, so a
	// Gateway can say it is waiting for an address rather than that it has none
	Addresses     []Address
	WantAddresses bool
}

// Write writes the input onto every named object still in cache at the generation and UID it
// was built from, returning how many writes failed; the context ending is not a failure
func (w *Writer) Write(ctx context.Context, in Input) int {
	if w == nil || in.Report == nil || w.cfg.Cache == nil {
		return 0
	}
	var failed int
	if w.cfg.GatewayClient != nil {
		failed += w.writeClasses(ctx, in)
		failed += w.writeGateways(ctx, in)
		failed += w.writeRoutes(ctx, in)
	}
	if w.cfg.Client != nil {
		failed += w.writeIngresses(ctx, in)
	}
	if w.cfg.DynamicClient != nil {
		failed += w.writePolicies(ctx, in)
	}
	return failed
}

func (w *Writer) writePolicies(ctx context.Context, in Input) int {
	var failed int
	for _, ps := range in.Report.Policies {
		if ctx.Err() != nil {
			return failed
		}
		obj := w.cfg.Cache.CachePolicy(ps.Source.Namespace, ps.Source.Name)
		if obj == nil {
			continue
		}
		api := w.cfg.DynamicClient.Resource(cachepolicy.GVR).Namespace(obj.Namespace)
		apply := func(o *cachepolicy.CachePolicy) { applyPolicy(o, ps, w.cfg.ControllerName) }
		if !write(ctx, w, ps.Source, obj, apply,
			func(ctx context.Context) (*cachepolicy.CachePolicy, error) {
				u, err := api.Get(ctx, obj.Name, metav1.GetOptions{})
				if err != nil {
					return nil, err
				}
				return cachepolicy.FromUnstructured(u)
			},
			func(ctx context.Context, o *cachepolicy.CachePolicy) error {
				u, err := cachepolicy.ToUnstructured(o)
				if err != nil {
					return err
				}
				_, err = api.UpdateStatus(ctx, u, metav1.UpdateOptions{})
				return err
			}) {
			failed++
		}
	}
	return failed
}

func (w *Writer) writeClasses(ctx context.Context, in Input) int {
	var failed int
	api := w.cfg.GatewayClient.GatewayV1().GatewayClasses()
	for _, c := range in.Report.Classes {
		if ctx.Err() != nil {
			return failed
		}
		obj := w.cfg.Cache.GatewayClass(c.Source.Name)
		if obj == nil {
			continue
		}
		apply := func(o *gwapiv1.GatewayClass) {
			meta.SetStatusCondition(&o.Status.Conditions, condition(c.Accepted, c.Source.Generation))
		}
		if !write(ctx, w, c.Source, obj, apply,
			func(ctx context.Context) (*gwapiv1.GatewayClass, error) {
				return api.Get(ctx, obj.Name, metav1.GetOptions{})
			},
			func(ctx context.Context, o *gwapiv1.GatewayClass) error {
				_, err := api.UpdateStatus(ctx, o, metav1.UpdateOptions{})
				return err
			}) {
			failed++
		}
	}
	return failed
}

func (w *Writer) writeGateways(ctx context.Context, in Input) int {
	var failed int
	for _, g := range in.Report.Gateways {
		if ctx.Err() != nil {
			return failed
		}
		obj := w.cfg.Cache.Gateway(g.Source.Namespace, g.Source.Name)
		if obj == nil {
			continue
		}
		api := w.cfg.GatewayClient.GatewayV1().Gateways(obj.Namespace)
		apply := func(o *gwapiv1.Gateway) { applyGateway(o, g, in) }
		if !write(ctx, w, g.Source, obj, apply,
			func(ctx context.Context) (*gwapiv1.Gateway, error) {
				return api.Get(ctx, obj.Name, metav1.GetOptions{})
			},
			func(ctx context.Context, o *gwapiv1.Gateway) error {
				_, err := api.UpdateStatus(ctx, o, metav1.UpdateOptions{})
				return err
			}) {
			failed++
		}
	}
	return failed
}

func (w *Writer) writeRoutes(ctx context.Context, in Input) int {
	// every route kind carries the same status, written through its own client
	var failed int
	gw := w.cfg.GatewayClient.GatewayV1()
	alpha := w.cfg.GatewayClient.GatewayV1alpha2()
	for _, r := range in.Report.Routes {
		if ctx.Err() != nil {
			return failed
		}
		ok := true
		switch r.Source.Kind {
		case ir.KindGRPCRoute:
			if obj := w.cfg.Cache.GRPCRoute(r.Source.Namespace, r.Source.Name); obj != nil {
				ok = writeRoute(ctx, w, r, obj, gw.GRPCRoutes(obj.Namespace),
					func(o *gwapiv1.GRPCRoute) *gwapiv1.RouteStatus { return &o.Status.RouteStatus })
			}
		case ir.KindTCPRoute:
			if obj := w.cfg.Cache.TCPRoute(r.Source.Namespace, r.Source.Name); obj != nil {
				ok = writeRoute(ctx, w, r, obj, alpha.TCPRoutes(obj.Namespace),
					func(o *gwapiv1a2.TCPRoute) *gwapiv1.RouteStatus { return &o.Status.RouteStatus })
			}
		case ir.KindTLSRoute:
			if obj := w.cfg.Cache.TLSRoute(r.Source.Namespace, r.Source.Name); obj != nil {
				ok = writeRoute(ctx, w, r, obj, alpha.TLSRoutes(obj.Namespace),
					func(o *gwapiv1a2.TLSRoute) *gwapiv1.RouteStatus { return &o.Status.RouteStatus })
			}
		case ir.KindUDPRoute:
			if obj := w.cfg.Cache.UDPRoute(r.Source.Namespace, r.Source.Name); obj != nil {
				ok = writeRoute(ctx, w, r, obj, alpha.UDPRoutes(obj.Namespace),
					func(o *gwapiv1a2.UDPRoute) *gwapiv1.RouteStatus { return &o.Status.RouteStatus })
			}
		default:
			if obj := w.cfg.Cache.HTTPRoute(r.Source.Namespace, r.Source.Name); obj != nil {
				ok = writeRoute(ctx, w, r, obj, gw.HTTPRoutes(obj.Namespace),
					func(o *gwapiv1.HTTPRoute) *gwapiv1.RouteStatus { return &o.Status.RouteStatus })
			}
		}
		if !ok {
			failed++
		}
	}
	return failed
}

// routeClient is the typed client a route kind's status is read and written through
type routeClient[P any] interface {
	Get(context.Context, string, metav1.GetOptions) (P, error)
	UpdateStatus(context.Context, P, metav1.UpdateOptions) (P, error)
}

func writeRoute[T any, P statusObject[T]](ctx context.Context, w *Writer, r ir.RouteStatus,
	cached P, api routeClient[P], status func(P) *gwapiv1.RouteStatus,
) bool {
	apply := func(o P) { applyRoute(status(o), r, w.cfg.ControllerName) }
	return write(ctx, w, r.Source, cached, apply,
		func(ctx context.Context) (P, error) {
			return api.Get(ctx, r.Source.Name, metav1.GetOptions{})
		},
		func(ctx context.Context, o P) error {
			_, err := api.UpdateStatus(ctx, o, metav1.UpdateOptions{})
			return err
		})
}

func (w *Writer) writeIngresses(ctx context.Context, in Input) int {
	// an Ingress's addresses are the only status written to it, so with no Service to publish
	// there is nothing to say and whatever another publisher wrote there is left alone
	if !in.WantAddresses {
		return 0
	}
	var failed int
	for _, src := range in.Report.Ingresses {
		if ctx.Err() != nil {
			return failed
		}
		obj := w.cfg.Cache.Ingress(src.Namespace, src.Name)
		if obj == nil {
			continue
		}
		api := w.cfg.Client.NetworkingV1().Ingresses(obj.Namespace)
		apply := func(o *netv1.Ingress) {
			o.Status.LoadBalancer.Ingress = ingressAddresses(in.Addresses)
		}
		if !write(ctx, w, src, obj, apply,
			func(ctx context.Context) (*netv1.Ingress, error) {
				return api.Get(ctx, obj.Name, metav1.GetOptions{})
			},
			func(ctx context.Context, o *netv1.Ingress) error {
				_, err := api.UpdateStatus(ctx, o, metav1.UpdateOptions{})
				return err
			}) {
			failed++
		}
	}
	return failed
}

// statusObject is what every kind written here has in common: a deep copy,
// a status to compare, and the identity and generation to check
type statusObject[T any] interface {
	*T
	DeepCopy() *T
	metav1.Object
}

func translated(obj metav1.Object, src ir.Source) bool {
	if src.UID != "" && string(obj.GetUID()) != src.UID {
		return false
	}
	return obj.GetGeneration() == src.Generation
}

func write[T any, P statusObject[T]](ctx context.Context, w *Writer, src ir.Source, cached P,
	apply func(P), get func(context.Context) (P, error), put func(context.Context, P) error,
) bool {
	if !translated(cached, src) {
		return true
	}
	desired := cached.DeepCopy()
	apply(desired)
	if equality.Semantic.DeepEqual(cached, desired) {
		return true
	}
	obj := desired
	var err error
	for attempt := range maxAttempts {
		if attempt > 0 {
			// another writer changed the object: re-read it and apply the same status again,
			// unless what changed was the object itself
			callCtx, cancel := w.callContext(ctx)
			obj, err = get(callCtx)
			cancel()
			if err != nil {
				break
			}
			if !translated(P(obj), src) {
				return true
			}
			apply(obj)
		}
		callCtx, cancel := w.callContext(ctx)
		err = put(callCtx, obj)
		cancel()
		if err == nil {
			return true
		}
		if !apierrors.IsConflict(err) {
			break
		}
	}
	if ctx.Err() != nil {
		// the term ended mid-write; its successor describes the object
		return true
	}
	metrics.KubeStatusWriteFailures.WithLabelValues(src.Kind).Inc()
	logger.Error("kubernetes status write failed", logging.Pairs{
		keys.Scope: kube.LogScope, keys.Kind: src.Kind, keys.Key: src.Key(),
		keys.Error: err.Error(),
	})
	return false
}

func (w *Writer) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if w.cfg.Timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, w.cfg.Timeout)
}

func condition(c ir.Condition, generation int64) metav1.Condition {
	status := metav1.ConditionFalse
	if c.Status {
		status = metav1.ConditionTrue
	}
	return metav1.Condition{
		Type: c.Type, Status: status, Reason: c.Reason, Message: c.Message,
		ObservedGeneration: generation,
	}
}

func programmed(c ir.Condition, in Input, gateway bool) ir.Condition {
	if c.Type != string(gwapiv1.GatewayConditionProgrammed) || !c.Status {
		return c
	}
	if !in.Programmed {
		return ir.Condition{
			Type: c.Type, Reason: string(gwapiv1.GatewayReasonPending),
			Message: "the generated configuration was not applied to the data plane",
		}
	}
	if gateway && in.WantAddresses && len(in.Addresses) == 0 {
		return ir.Condition{
			Type: c.Type, Reason: string(gwapiv1.GatewayReasonAddressNotAssigned),
			Message: "the published service has no address yet",
		}
	}
	return c
}

func applyGateway(o *gwapiv1.Gateway, g ir.GatewayStatus, in Input) {
	generation := g.Source.Generation
	for _, c := range g.Conditions {
		meta.SetStatusCondition(&o.Status.Conditions, condition(programmed(c, in, true), generation))
	}
	o.Status.Addresses = gatewayAddresses(in.Addresses)
	previous := make(map[string][]metav1.Condition, len(o.Status.Listeners))
	for _, l := range o.Status.Listeners {
		previous[string(l.Name)] = l.Conditions
	}
	listeners := make([]gwapiv1.ListenerStatus, 0, len(g.Listeners))
	for _, l := range g.Listeners {
		ls := gwapiv1.ListenerStatus{
			Name:           gwapiv1.SectionName(l.Name),
			AttachedRoutes: int32(l.AttachedRoutes), // #nosec G115 -- bounded by the object's own listener count
			Conditions:     slices.Clone(previous[l.Name]),
		}
		for _, k := range l.SupportedKinds {
			group := gwapiv1.Group(gwapiv1.GroupName)
			ls.SupportedKinds = append(ls.SupportedKinds,
				gwapiv1.RouteGroupKind{Group: &group, Kind: gwapiv1.Kind(k)})
		}
		for _, c := range l.Conditions {
			meta.SetStatusCondition(&ls.Conditions, condition(programmed(c, in, false), generation))
		}
		listeners = append(listeners, ls)
	}
	o.Status.Listeners = listeners
}

func applyRoute(o *gwapiv1.RouteStatus, r ir.RouteStatus, controllerName string) {
	// entries of other controllers are kept as they are; ours are rebuilt from the report,
	// which also drops an entry for a parent the route no longer names
	name := gwapiv1.GatewayController(controllerName)
	kept, previous := foreignEntries(o.Parents, name,
		func(p gwapiv1.RouteParentStatus) (gwapiv1.GatewayController, gwapiv1.ParentReference,
			[]metav1.Condition,
		) {
			return p.ControllerName, p.ParentRef, p.Conditions
		})
	for _, p := range r.Parents {
		ref := parentRef(p.Ref)
		kept = append(kept, gwapiv1.RouteParentStatus{
			ParentRef: ref, ControllerName: name,
			Conditions: rebuilt(previous[parentKey(ref)], p.Conditions, r.Source.Generation),
		})
	}
	o.Parents = kept
}

func applyPolicy(o *cachepolicy.CachePolicy, ps ir.PolicyStatus, controllerName string) {
	// one entry per targetRef the policy declares, under this controller's name
	name := gwapiv1.GatewayController(controllerName)
	kept, previous := foreignEntries(o.Status.Ancestors, name,
		func(a gwapiv1.PolicyAncestorStatus) (gwapiv1.GatewayController, gwapiv1.ParentReference,
			[]metav1.Condition,
		) {
			return a.ControllerName, a.AncestorRef, a.Conditions
		})
	for _, a := range ps.Ancestors {
		ref := parentRef(a.Ref)
		kept = append(kept, gwapiv1.PolicyAncestorStatus{
			AncestorRef: ref, ControllerName: name,
			Conditions: rebuilt(previous[parentKey(ref)], a.Conditions, ps.Source.Generation),
		})
	}
	o.Status.Ancestors = kept
}

// foreignEntries returns the per-parent entries other controllers wrote, kept as they are, and
// this controller's previous conditions by parent reference, so a rebuilt entry keeps the
// transition time of a condition whose status held
func foreignEntries[E any](entries []E, name gwapiv1.GatewayController,
	fields func(E) (gwapiv1.GatewayController, gwapiv1.ParentReference, []metav1.Condition),
) ([]E, map[string][]metav1.Condition) {
	kept := make([]E, 0, len(entries))
	previous := make(map[string][]metav1.Condition)
	for _, e := range entries {
		owner, ref, conditions := fields(e)
		if owner != name {
			kept = append(kept, e)
			continue
		}
		previous[parentKey(ref)] = conditions
	}
	return kept, previous
}

func rebuilt(previous []metav1.Condition, wanted []ir.Condition, generation int64) []metav1.Condition {
	out := slices.Clone(previous)
	for _, c := range wanted {
		meta.SetStatusCondition(&out, condition(c, generation))
	}
	return out
}

func parentRef(r ir.ParentRef) gwapiv1.ParentReference {
	out := gwapiv1.ParentReference{Name: gwapiv1.ObjectName(r.Name)}
	if r.Group != "" {
		g := gwapiv1.Group(r.Group)
		out.Group = &g
	}
	if r.Kind != "" {
		k := gwapiv1.Kind(r.Kind)
		out.Kind = &k
	}
	if r.Namespace != "" {
		ns := gwapiv1.Namespace(r.Namespace)
		out.Namespace = &ns
	}
	if r.SectionName != "" {
		s := gwapiv1.SectionName(r.SectionName)
		out.SectionName = &s
	}
	if r.Port != 0 {
		p := gwapiv1.PortNumber(r.Port) // #nosec G115 -- a port number is validated by the API server
		out.Port = &p
	}
	return out
}

func parentKey(r gwapiv1.ParentReference) string {
	var port string
	if r.Port != nil {
		port = strconv.Itoa(int(*r.Port))
	}
	return strings.Join([]string{
		deref(r.Group), deref(r.Kind), deref(r.Namespace), string(r.Name),
		deref(r.SectionName), port,
	}, "|")
}

func deref[T ~string](p *T) string {
	if p == nil {
		return ""
	}
	return string(*p)
}

func gatewayAddresses(in []Address) []gwapiv1.GatewayStatusAddress {
	if len(in) == 0 {
		return nil
	}
	out := make([]gwapiv1.GatewayStatusAddress, 0, len(in))
	for _, a := range in {
		t := gwapiv1.AddressType(a.Type)
		out = append(out, gwapiv1.GatewayStatusAddress{Type: &t, Value: a.Value})
	}
	return out
}

func ingressAddresses(in []Address) []netv1.IngressLoadBalancerIngress {
	if len(in) == 0 {
		return nil
	}
	out := make([]netv1.IngressLoadBalancerIngress, 0, len(in))
	for _, a := range in {
		if a.Type == AddressHostname {
			out = append(out, netv1.IngressLoadBalancerIngress{Hostname: a.Value})
			continue
		}
		out = append(out, netv1.IngressLoadBalancerIngress{IP: a.Value})
	}
	return out
}
