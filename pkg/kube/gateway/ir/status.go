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

package ir

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/proxy/hostnames"
)

// Condition is one status condition as a translator states it: the vocabulary is the Kubernetes
// API's, but the shape is independent of its types so the writer alone depends on them
type Condition struct {
	// Type is the condition type (Accepted, Programmed, ResolvedRefs, ...)
	Type string `json:"type"`
	// Status is true for a condition that holds
	Status bool `json:"status"`
	// Reason is the machine-readable explanation
	Reason string `json:"reason"`
	// Message explains the reason to a person
	Message string `json:"message,omitempty"`
}

// ClassStatus is what is written back to a claimed GatewayClass
type ClassStatus struct {
	Source   Source    `json:"source"`
	Accepted Condition `json:"accepted"`
}

// ListenerStatus is one Gateway listener's status, whether or not it was accepted
type ListenerStatus struct {
	// Name is the listener's section name
	Name string `json:"name"`
	// SupportedKinds are the route kinds the listener admits
	SupportedKinds []string `json:"supported_kinds,omitempty"`
	// AttachedRoutes counts the routes accepted on the listener
	AttachedRoutes int `json:"attached_routes"`
	// Conditions are Accepted, Programmed, ResolvedRefs and Conflicted
	Conditions []Condition `json:"conditions,omitempty"`
}

// GatewayStatus is what is written back to a claimed Gateway
type GatewayStatus struct {
	Source Source `json:"source"`
	// Conditions are Accepted and Programmed
	Conditions []Condition `json:"conditions,omitempty"`
	// Listeners carries one entry per listener the Gateway declared, in declaration order
	Listeners []ListenerStatus `json:"listeners,omitempty"`
}

// ParentRef identifies one parentRef of a route as the route declared it,
// which is how its status entry is matched back to the spec
type ParentRef struct {
	Group       string `json:"group,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name"`
	SectionName string `json:"section_name,omitempty"`
	Port        int    `json:"port,omitempty"`
}

// ParentStatus is a route's status with respect to one claimed parent
type ParentStatus struct {
	Ref ParentRef `json:"ref"`
	// Conditions are Accepted and ResolvedRefs
	Conditions []Condition `json:"conditions,omitempty"`
}

// RouteStatus is what is written back to a route, one entry per parentRef
// naming a Gateway this controller claims
type RouteStatus struct {
	Source  Source         `json:"source"`
	Parents []ParentStatus `json:"parents,omitempty"`
}

// AncestorStatus is a policy's status with respect to one of the targets it names
type AncestorStatus struct {
	Ref ParentRef `json:"ref"`
	// Conditions are Accepted, and whatever else the policy's kind defines
	Conditions []Condition `json:"conditions,omitempty"`
}

// PolicyStatus is what is written back to a cache policy: one entry per targetRef it declared
type PolicyStatus struct {
	Source    Source           `json:"source"`
	Ancestors []AncestorStatus `json:"ancestors,omitempty"`
}

// Report is everything a translation pass has to say about the objects it read: the status the
// leader writes back, and which objects receive the published addresses
type Report struct {
	Classes  []ClassStatus   `json:"classes,omitempty"`
	Gateways []GatewayStatus `json:"gateways,omitempty"`
	Routes   []RouteStatus   `json:"routes,omitempty"`
	// Ingresses are the claimed Ingress objects, whose status carries only
	// the published addresses
	Ingresses []Source `json:"ingresses,omitempty"`
	// Policies are the cache policies read, whatever their targets
	Policies []PolicyStatus `json:"policies,omitempty"`
}

// Merge returns one Report holding everything in both
func (r *Report) Merge(o *Report) *Report {
	if r == nil {
		r = &Report{}
	}
	if o == nil {
		return r
	}
	return &Report{
		Classes:   append(slices.Clone(r.Classes), o.Classes...),
		Gateways:  append(slices.Clone(r.Gateways), o.Gateways...),
		Routes:    append(slices.Clone(r.Routes), o.Routes...),
		Ingresses: append(slices.Clone(r.Ingresses), o.Ingresses...),
		Policies:  append(slices.Clone(r.Policies), o.Policies...),
	}
}

// IsEmpty reports whether the report names no object at all
func (r *Report) IsEmpty() bool {
	return r == nil || (len(r.Classes) == 0 && len(r.Gateways) == 0 &&
		len(r.Routes) == 0 && len(r.Ingresses) == 0 && len(r.Policies) == 0)
}

// Find returns the condition of the given type, and false when there is none
func Find(conditions []Condition, typ string) (Condition, bool) {
	for _, c := range conditions {
		if c.Type == typ {
			return c, true
		}
	}
	return Condition{}, false
}

// Set replaces the condition of the same type, or appends one
func Set(conditions []Condition, c Condition) []Condition {
	for i := range conditions {
		if conditions[i].Type == c.Type {
			conditions[i] = c
			return conditions
		}
	}
	return append(conditions, c)
}

// Certificate material problems ValidateCertPair reports
var (
	// ErrCertEmpty indicates a TLS Secret missing its certificate or key
	ErrCertEmpty = errors.New("secret is missing its certificate or key")
	// ErrCertInvalid indicates certificate material the data plane could not serve
	ErrCertInvalid = errors.New("secret holds an unusable certificate pair")
)

// CertIdentity is what a served certificate answers for: the names its leaf carries, normalized
// as a certificate store indexes them, and a digest of its chain so one held twice is one
type CertIdentity struct {
	Names  []string
	Digest string
}

// ParseCertPair reports whether a certificate and key can be served together, and what the
// certificate answers for when they can; material that cannot be parsed would fail every handshake
func ParseCertPair(crt, key []byte) (CertIdentity, error) {
	if len(crt) == 0 || len(key) == 0 {
		return CertIdentity{}, ErrCertEmpty
	}
	pair, err := tls.X509KeyPair(crt, key)
	if err != nil {
		return CertIdentity{}, fmt.Errorf("%w: %w", ErrCertInvalid, err)
	}
	return certIdentity(pair), nil
}

// ValidateCertPair reports whether a certificate and key can be served together
func ValidateCertPair(crt, key []byte) error {
	_, err := ParseCertPair(crt, key)
	return err
}

func certIdentity(pair tls.Certificate) CertIdentity {
	h := sha256.New()
	for _, der := range pair.Certificate {
		h.Write(der)
	}
	id := CertIdentity{Digest: hex.EncodeToString(h.Sum(nil))}
	leaf := pair.Leaf
	if leaf == nil && len(pair.Certificate) > 0 {
		leaf, _ = x509.ParseCertificate(pair.Certificate[0])
	}
	if leaf == nil {
		return id
	}
	// the store indexes the subject alternative names, falling back to the common name, and
	// answers for a wildcard by its suffix, so a wildcard is a name of its own here
	names := leaf.DNSNames
	if len(names) == 0 && leaf.Subject.CommonName != "" {
		names = []string{leaf.Subject.CommonName}
	}
	for _, name := range names {
		n, err := hostnames.Normalize(name, hostnames.RequireHost)
		if err != nil || hostnames.IsAnyDepth(n) {
			continue
		}
		id.Names = append(id.Names, n)
	}
	slices.Sort(id.Names)
	id.Names = slices.Compact(id.Names)
	return id
}
