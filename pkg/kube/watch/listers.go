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
	"errors"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/cachepolicy"

	corev1 "k8s.io/api/core/v1"
	netv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

// The accessors below return everything the watcher holds for a kind, in namespace/name order
// and filtered by the namespace selector, so a translator never calls the API server

// namespaced is the shape every namespaced Kubernetes object shares
type namespaced interface {
	GetNamespace() string
	GetName() string
}

func collect[T namespaced](w *Watcher, list func(*scope) ([]T, error)) []T {
	var out []T
	for _, s := range w.scopes {
		items, err := list(s)
		if err != nil {
			continue
		}
		for _, item := range items {
			if w.NamespaceAllowed(item.GetNamespace()) {
				out = append(out, item)
			}
		}
	}
	slices.SortFunc(out, func(a, b T) int {
		if c := strings.Compare(a.GetNamespace(), b.GetNamespace()); c != 0 {
			return c
		}
		return strings.Compare(a.GetName(), b.GetName())
	})
	return out
}

// NamespaceAllowed reports whether the namespace selector admits a namespace; with no selector
// every namespace is admitted, and one whose object is not in cache is not
func (w *Watcher) NamespaceAllowed(namespace string) bool {
	if w.selector == nil {
		return true
	}
	if w.cluster == nil || w.cluster.namespaces == nil {
		return false
	}
	ns, err := w.cluster.namespaces.Get(namespace)
	if err != nil || ns == nil {
		return false
	}
	return w.selector.Matches(labels.Set(ns.Labels))
}

// The Gateway API accessors below return nothing in a cluster that does not serve the Gateway
// API: no informer was built for those kinds, so their listers are nil.

// Gateways returns every Gateway in scope
func (w *Watcher) Gateways() []*gwapiv1.Gateway {
	return collect(w, func(s *scope) ([]*gwapiv1.Gateway, error) {
		if s.gateways == nil {
			return nil, nil
		}
		return s.gateways.List(labels.Everything())
	})
}

// HTTPRoutes returns every HTTPRoute in scope
func (w *Watcher) HTTPRoutes() []*gwapiv1.HTTPRoute {
	return collect(w, func(s *scope) ([]*gwapiv1.HTTPRoute, error) {
		if s.routes == nil {
			return nil, nil
		}
		return s.routes.List(labels.Everything())
	})
}

// GRPCRoutes returns every GRPCRoute in scope, or nothing in a cluster that does not serve
// the kind
func (w *Watcher) GRPCRoutes() []*gwapiv1.GRPCRoute {
	return collect(w, func(s *scope) ([]*gwapiv1.GRPCRoute, error) {
		if s.grpcRoutes == nil {
			return nil, nil
		}
		return s.grpcRoutes.List(labels.Everything())
	})
}

// TCPRoutes returns every TCPRoute in scope, or nothing in a cluster that does not serve the kind
func (w *Watcher) TCPRoutes() []*gwapiv1a2.TCPRoute {
	return collect(w, func(s *scope) ([]*gwapiv1a2.TCPRoute, error) {
		if s.tcpRoutes == nil {
			return nil, nil
		}
		return s.tcpRoutes.List(labels.Everything())
	})
}

// TLSRoutes returns every TLSRoute in scope, or nothing in a cluster that does not serve the kind
func (w *Watcher) TLSRoutes() []*gwapiv1a2.TLSRoute {
	return collect(w, func(s *scope) ([]*gwapiv1a2.TLSRoute, error) {
		if s.tlsRoutes == nil {
			return nil, nil
		}
		return s.tlsRoutes.List(labels.Everything())
	})
}

// UDPRoutes returns every UDPRoute in scope, or nothing in a cluster that does not serve the kind
func (w *Watcher) UDPRoutes() []*gwapiv1a2.UDPRoute {
	return collect(w, func(s *scope) ([]*gwapiv1a2.UDPRoute, error) {
		if s.udpRoutes == nil {
			return nil, nil
		}
		return s.udpRoutes.List(labels.Everything())
	})
}

// ReferenceGrants returns every ReferenceGrant in scope. They are what
// permits a cross-namespace Service or Secret reference.
func (w *Watcher) ReferenceGrants() []*gwapiv1.ReferenceGrant {
	return collect(w, func(s *scope) ([]*gwapiv1.ReferenceGrant, error) {
		if s.grants == nil {
			return nil, nil
		}
		return s.grants.List(labels.Everything())
	})
}

// BackendTLSPolicies returns every BackendTLSPolicy in scope, or nothing in
// a cluster that does not serve the kind
func (w *Watcher) BackendTLSPolicies() []*gwapiv1.BackendTLSPolicy {
	return collect(w, func(s *scope) ([]*gwapiv1.BackendTLSPolicy, error) {
		if s.backendTLS == nil {
			return nil, nil
		}
		return s.backendTLS.List(labels.Everything())
	})
}

// ServesGatewayAPI reports whether this watcher holds the Gateway API kinds
func (w *Watcher) ServesGatewayAPI() bool {
	return w.cfg.GatewayClient != nil
}

// ServesCachePolicies reports whether this watcher holds the cache policy resource
func (w *Watcher) ServesCachePolicies() bool {
	return w.cfg.DynamicClient != nil
}

// CachePolicies returns every cache policy in scope, or nothing in a cluster that does not
// serve the resource
func (w *Watcher) CachePolicies() []*cachepolicy.CachePolicy {
	return collect(w, func(s *scope) ([]*cachepolicy.CachePolicy, error) {
		if s.policies == nil {
			return nil, nil
		}
		return s.policies.list(), nil
	})
}

// CachePolicy returns one cache policy from cache, or nil when it is absent, out of scope, or
// the cluster does not serve the resource
func (w *Watcher) CachePolicy(namespace, name string) *cachepolicy.CachePolicy {
	return lookup(w, namespace, func(s *scope) (*cachepolicy.CachePolicy, error) {
		if s.policies == nil {
			return nil, errNoLister
		}
		if cp := s.policies.get(namespace, name); cp != nil {
			return cp, nil
		}
		return nil, apierrors.NewNotFound(cachepolicy.GVR.GroupResource(), name)
	})
}

// Ingresses returns every Ingress in scope
func (w *Watcher) Ingresses() []*netv1.Ingress {
	return collect(w, func(s *scope) ([]*netv1.Ingress, error) {
		return s.ingresses.List(labels.Everything())
	})
}

// Services returns every Service in scope
func (w *Watcher) Services() []*corev1.Service {
	return collect(w, func(s *scope) ([]*corev1.Service, error) {
		return s.services.List(labels.Everything())
	})
}

// Secrets returns every TLS Secret in scope
func (w *Watcher) Secrets() []*corev1.Secret {
	return collect(w, func(s *scope) ([]*corev1.Secret, error) {
		return s.secretLst.List(labels.Everything())
	})
}

// GatewayClasses returns every GatewayClass in the cluster; claiming
// narrows them to this controller's
func (w *Watcher) GatewayClasses() []*gwapiv1.GatewayClass {
	if w.cluster == nil || w.cluster.gatewayClasses == nil {
		return nil
	}
	out, err := w.cluster.gatewayClasses.List(labels.Everything())
	if err != nil {
		return nil
	}
	slices.SortFunc(out, func(a, b *gwapiv1.GatewayClass) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// IngressClasses returns every IngressClass in the cluster
func (w *Watcher) IngressClasses() []*netv1.IngressClass {
	if w.cluster == nil || w.cluster.ingressClasses == nil {
		return nil
	}
	out, err := w.cluster.ingressClasses.List(labels.Everything())
	if err != nil {
		return nil
	}
	slices.SortFunc(out, func(a, b *netv1.IngressClass) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// errNoLister marks a kind the watcher holds no informer for
var errNoLister = errors.New("kind is not watched")

func lookup[T any](w *Watcher, namespace string, get func(*scope) (*T, error)) *T {
	if !w.NamespaceAllowed(namespace) {
		return nil
	}
	for _, s := range w.scopes {
		if s.namespace != "" && s.namespace != namespace {
			continue
		}
		obj, err := get(s)
		if err == nil {
			return obj
		}
		if !apierrors.IsNotFound(err) {
			return nil
		}
	}
	return nil
}

// Service returns one Service from cache, or nil when it is absent or out of scope, so an
// unresolvable backendRef is a translation decision rather than an API call
func (w *Watcher) Service(namespace, name string) *corev1.Service {
	return lookup(w, namespace, func(s *scope) (*corev1.Service, error) {
		return s.services.Services(namespace).Get(name)
	})
}

// Secret returns one TLS Secret from cache, or nil when it is absent, not a
// TLS secret, or out of scope
func (w *Watcher) Secret(namespace, name string) *corev1.Secret {
	return lookup(w, namespace, func(s *scope) (*corev1.Secret, error) {
		return s.secretLst.Secrets(namespace).Get(name)
	})
}

// ConfigMap returns one ConfigMap from cache, or nil when it is absent, out of scope, or the
// cluster does not serve the Gateway API, the only reader of ConfigMaps
func (w *Watcher) ConfigMap(namespace, name string) *corev1.ConfigMap {
	return lookup(w, namespace, func(s *scope) (*corev1.ConfigMap, error) {
		if s.configMaps == nil {
			return nil, errNoLister
		}
		return s.configMaps.ConfigMaps(namespace).Get(name)
	})
}

// Ingress returns one Ingress from cache, or nil when it is absent or out of scope
func (w *Watcher) Ingress(namespace, name string) *netv1.Ingress {
	return lookup(w, namespace, func(s *scope) (*netv1.Ingress, error) {
		return s.ingresses.Ingresses(namespace).Get(name)
	})
}

// Gateway returns one Gateway from cache, or nil when it is absent, out of
// scope, or the cluster does not serve the Gateway API
func (w *Watcher) Gateway(namespace, name string) *gwapiv1.Gateway {
	return lookup(w, namespace, func(s *scope) (*gwapiv1.Gateway, error) {
		if s.gateways == nil {
			return nil, errNoLister
		}
		return s.gateways.Gateways(namespace).Get(name)
	})
}

// HTTPRoute returns one HTTPRoute from cache, or nil when it is absent, out
// of scope, or the cluster does not serve the Gateway API
func (w *Watcher) HTTPRoute(namespace, name string) *gwapiv1.HTTPRoute {
	return lookup(w, namespace, func(s *scope) (*gwapiv1.HTTPRoute, error) {
		if s.routes == nil {
			return nil, errNoLister
		}
		return s.routes.HTTPRoutes(namespace).Get(name)
	})
}

// GRPCRoute returns one GRPCRoute from cache, or nil when it is absent, out of scope, or the
// cluster does not serve the kind
func (w *Watcher) GRPCRoute(namespace, name string) *gwapiv1.GRPCRoute {
	return lookup(w, namespace, func(s *scope) (*gwapiv1.GRPCRoute, error) {
		if s.grpcRoutes == nil {
			return nil, errNoLister
		}
		return s.grpcRoutes.GRPCRoutes(namespace).Get(name)
	})
}

// TCPRoute returns one TCPRoute from cache, or nil when it is absent, out of scope, or the
// cluster does not serve the kind
func (w *Watcher) TCPRoute(namespace, name string) *gwapiv1a2.TCPRoute {
	return lookup(w, namespace, func(s *scope) (*gwapiv1a2.TCPRoute, error) {
		if s.tcpRoutes == nil {
			return nil, errNoLister
		}
		return s.tcpRoutes.TCPRoutes(namespace).Get(name)
	})
}

// TLSRoute returns one TLSRoute from cache, or nil when it is absent, out of scope, or the
// cluster does not serve the kind
func (w *Watcher) TLSRoute(namespace, name string) *gwapiv1a2.TLSRoute {
	return lookup(w, namespace, func(s *scope) (*gwapiv1a2.TLSRoute, error) {
		if s.tlsRoutes == nil {
			return nil, errNoLister
		}
		return s.tlsRoutes.TLSRoutes(namespace).Get(name)
	})
}

// UDPRoute returns one UDPRoute from cache, or nil when it is absent, out of scope, or the
// cluster does not serve the kind
func (w *Watcher) UDPRoute(namespace, name string) *gwapiv1a2.UDPRoute {
	return lookup(w, namespace, func(s *scope) (*gwapiv1a2.UDPRoute, error) {
		if s.udpRoutes == nil {
			return nil, errNoLister
		}
		return s.udpRoutes.UDPRoutes(namespace).Get(name)
	})
}

// GatewayClass returns one GatewayClass from cache, or nil when it is absent
// or the cluster does not serve the Gateway API
func (w *Watcher) GatewayClass(name string) *gwapiv1.GatewayClass {
	if w.cluster == nil || w.cluster.gatewayClasses == nil {
		return nil
	}
	gc, err := w.cluster.gatewayClasses.Get(name)
	if err != nil {
		return nil
	}
	return gc
}

// PublishedService returns the Service whose addresses are published into status, or nil when
// none is configured or it is absent
func (w *Watcher) PublishedService() *corev1.Service {
	ps := w.cfg.Options.PublishedService
	if w.published == nil || ps == nil {
		return nil
	}
	svc, err := w.published.services.Services(ps.Namespace).Get(ps.Name)
	if err != nil {
		return nil
	}
	return svc
}

// Namespace returns one Namespace from cache, or nil when it is absent or namespaces are not
// watched, which they are only under a namespace selector or the Gateway API
func (w *Watcher) Namespace(name string) *corev1.Namespace {
	if w.cluster == nil || w.cluster.namespaces == nil {
		return nil
	}
	ns, err := w.cluster.namespaces.Get(name)
	if err != nil {
		return nil
	}
	return ns
}

// HasSynced reports whether the watcher's caches have completed their
// initial sync
func (w *Watcher) HasSynced() bool {
	w.mtx.Lock()
	defer w.mtx.Unlock()
	return w.synced
}
