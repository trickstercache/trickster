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

// Package translate holds the mechanics the Ingress and Gateway translators share: problems,
// object identity and order, Service ports, TLS Secrets, hostnames and policy values
package translate

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Problems collects what a translator could not do, in order; with
// deduplication, one reference resolved on several frontages is reported once
type Problems struct {
	list []ir.Problem
	seen map[string]struct{}
}

// NewProblems returns an empty collector, deduplicating when dedupe is set
func NewProblems(dedupe bool) *Problems {
	p := &Problems{}
	if dedupe {
		p.seen = make(map[string]struct{})
	}
	return p
}

// Reject records one problem against a source
func (p *Problems) Reject(src ir.Source, format string, args ...any) {
	p.RejectAs("", src, format, args...)
}

// RejectAs records one problem against a source under an Event reason
func (p *Problems) RejectAs(reason string, src ir.Source, format string, args ...any) {
	problem := ir.Problem{Source: src, Detail: fmt.Sprintf(format, args...), Reason: reason}
	if p.seen != nil {
		key := problem.String()
		if _, dup := p.seen[key]; dup {
			return
		}
		p.seen[key] = struct{}{}
	}
	p.list = append(p.list, problem)
}

// List returns the problems recorded so far
func (p *Problems) List() []ir.Problem {
	if p == nil {
		return nil
	}
	return p.list
}

// Source identifies a Kubernetes object within the IR by the given kind
func Source(kind string, obj metav1.Object) ir.Source {
	return ir.Source{
		Kind: kind, Namespace: obj.GetNamespace(),
		Name: obj.GetName(), Generation: obj.GetGeneration(),
		UID: string(obj.GetUID()),
	}
}

// ByAge orders objects oldest first, then by namespace and name, so every
// replica awards conflicts identically
func ByAge[T metav1.Object](in []T) []T {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b T) int {
		if c := a.GetCreationTimestamp().Time.Compare(
			b.GetCreationTimestamp().Time); c != 0 {
			return c
		}
		if c := strings.Compare(a.GetNamespace(), b.GetNamespace()); c != 0 {
			return c
		}
		return strings.Compare(a.GetName(), b.GetName())
	})
	return out
}

// CoreCache is the read side both translators need: Services and TLS
// Secrets, served from informer caches
type CoreCache interface {
	Service(namespace, name string) *corev1.Service
	Secret(namespace, name string) *corev1.Secret
}

// NamespacedName is the stable namespace/name key objects are indexed by
func NamespacedName(namespace, name string) string {
	return namespace + "/" + name
}

// PortRef names a Service port by name or, when the name is empty, by number
type PortRef struct {
	// Protocol is the transport the port must carry; empty accepts any
	Protocol corev1.Protocol
	Name     string
	Number   int32
}

func (r PortRef) String() string {
	if r.Name != "" {
		return strconv.Quote(r.Name)
	}
	return strconv.Itoa(int(r.Number))
}

// ServicePort resolves a port reference against a Service; the generated origin names the port
// directly, so an unmatched reference would be a connection refused per request. A Service may
// expose one port number over TCP and UDP with different targets, so the reference's transport
// decides which; a port declaring no protocol is TCP.
func ServicePort(svc *corev1.Service, ref PortRef) (corev1.ServicePort, bool) {
	for _, p := range svc.Spec.Ports {
		if ref.Protocol != "" && portProtocol(p) != ref.Protocol {
			continue
		}
		if ref.Name != "" && p.Name == ref.Name {
			return p, true
		}
		if ref.Name == "" && ref.Number != 0 && p.Port == ref.Number {
			return p, true
		}
	}
	return corev1.ServicePort{}, false
}

func portProtocol(p corev1.ServicePort) corev1.Protocol {
	if p.Protocol == "" {
		return corev1.ProtocolTCP
	}
	return p.Protocol
}

// ErrSecretNotFound indicates a Secret the cache does not hold: missing,
// out of scope, or not a kubernetes.io/tls Secret
var ErrSecretNotFound = errors.New("secret not found, or not a kubernetes.io/tls secret")

// CertRefs records the TLS Secrets a model references, each once, with the verdict on its material
// so a Secret several listeners name is judged once
type CertRefs struct {
	// Judge judges a Secret's material and reads what it answers for; nil parses it, a caller
	// may supply a cached judge
	Judge func(*corev1.Secret) (ir.CertIdentity, error)
	// Serving answers what serves under a Secret on one listener when its material is unusable,
	// since unusable material withdraws nothing from a store; nil means nothing does
	Serving func(listener ir.Listener, key string) (ir.CertIdentity, bool)
	results map[string]certResult
}

// certResult is one Secret's verdict: what its certificate answers for, or why it cannot be served
type certResult struct {
	identity ir.CertIdentity
	err      error
}

// Collect records the Secret as a CertRef the first time it is named and judges its pair,
// returning its key and why it cannot be served; unusable material is still recorded
func (c *CertRefs) Collect(cache CoreCache, model *ir.IR, src ir.Source,
	namespace, name string,
) (string, error) {
	key := NamespacedName(namespace, name)
	if c.results == nil {
		c.results = make(map[string]certResult)
	}
	if r, seen := c.results[key]; seen {
		return key, r.err
	}
	sec := cache.Secret(namespace, name)
	if sec == nil {
		c.results[key] = certResult{err: ErrSecretNotFound}
		return key, ErrSecretNotFound
	}
	judge := c.Judge
	if judge == nil {
		judge = func(s *corev1.Secret) (ir.CertIdentity, error) {
			return ir.ParseCertPair(s.Data[corev1.TLSCertKey], s.Data[corev1.TLSPrivateKeyKey])
		}
	}
	identity, err := judge(sec)
	c.results[key] = certResult{identity: identity, err: err}
	model.Certs = append(model.Certs, ir.CertRef{
		Name: key, Namespace: namespace, SecretName: name, Source: src,
	})
	return key, err
}

// Identity returns what serves under a collected Secret on a listener: its material where usable,
// and otherwise whatever certificate the listener's store still holds under it
func (c *CertRefs) Identity(listener ir.Listener, key string) ir.CertIdentity {
	r, ok := c.results[key]
	if !ok {
		return ir.CertIdentity{}
	}
	if r.err == nil || c.Serving == nil {
		return r.identity
	}
	id, _ := c.Serving(listener, key)
	return id
}

// Hostname modes: an Ingress rule may omit its host, a Gateway hostname may
// not, and a hostname sent as SNI or placed in a Location must be precise
const (
	HostnameAllowEmpty = hostnames.AllowEmpty
	HostnameRequired   = hostnames.RequireHost
	HostnamePrecise    = hostnames.RequirePrecise
)

var (
	ErrEmptyHostname    = errors.New("hostname is empty")
	ErrBadWildcard      = errors.New("a wildcard hostname may only be a leading '*.' label")
	ErrWildcardHostname = errors.New("hostname must be precise; a wildcard is not allowed")
)

// Hostname normalizes a hostname under a mode. Both APIs define a wildcard
// as one leading '*.' label, so the router's any-depth spelling is refused.
func Hostname(h string, mode hostnames.Mode) (string, error) {
	n, err := hostnames.Normalize(h, mode)
	switch {
	case errors.Is(err, hostnames.ErrEmpty):
		return "", ErrEmptyHostname
	case errors.Is(err, hostnames.ErrPrecise):
		return "", fmt.Errorf("%w (got %q)", ErrWildcardHostname, h)
	case errors.Is(err, hostnames.ErrWildcard), hostnames.IsAnyDepth(n):
		return "", ErrBadWildcard
	}
	return n, err
}

// Unique returns a sorted copy without repeats, nil for an empty input
func Unique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

// CheckName reports whether the running configuration defines a named
// object; a nil set accepts any name, and an empty name names nothing
func CheckName(known sets.Set[string], kind, name string) error {
	if name == "" || known == nil || known.Contains(name) {
		return nil
	}
	return fmt.Errorf("no %s named %q is configured", kind, name)
}

// GroupOf returns a reference's group, reading an absent one as the core group
func GroupOf(g *gwapiv1.Group) string {
	if g == nil {
		return ""
	}
	return string(*g)
}

// KindOf returns a reference's kind, or the default when it names none
func KindOf(k *gwapiv1.Kind, def string) string {
	if k == nil || *k == "" {
		return def
	}
	return string(*k)
}

// NamespaceOf returns a reference's namespace, or the default when it names none
func NamespaceOf(ns *gwapiv1.Namespace, def string) string {
	if ns == nil || *ns == "" {
		return def
	}
	return string(*ns)
}
