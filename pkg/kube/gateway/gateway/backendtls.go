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

package gateway

import (
	"errors"
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/internal/translate"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"
	tlso "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// caCertificateKey is the key a CA bundle is read from in a ConfigMap or Secret
const caCertificateKey = "ca.crt"

// wellKnownSystemCAs is the one well-known certificate set this build knows
const wellKnownSystemCAs = "System"

// Reasons a BackendTLSPolicy cannot be honored
var (
	errTLSNoValidation   = errors.New("validation names neither caCertificateRefs nor wellKnownCACertificates")
	errTLSBothValidation = errors.New("validation names both caCertificateRefs and wellKnownCACertificates")
	errTLSSubjectAltName = errors.New("subjectAltNames are not supported")
	errTLSWellKnown      = errors.New("unsupported wellKnownCACertificates value")
	errTLSCARefKind      = errors.New("caCertificateRef kind is not supported")
	errTLSCARefMissing   = errors.New("caCertificateRef not found")
	errTLSCARefKey       = errors.New("caCertificateRef has no " + caCertificateKey + " key")
	errTLSCARefPEM       = errors.New("caCertificateRef " + caCertificateKey + " holds no parsable certificate")
)

// backendTLSIndex maps a Service, or one named port of it, to the BackendTLSPolicy governing its
// connections, memoizing each policy's lowering since several backendRefs may name one Service
type backendTLSIndex struct {
	byTarget map[string]*gwapiv1.BackendTLSPolicy
	lowered  map[string]*ir.BackendTLS
	failed   map[string]string
}

func policySource(p *gwapiv1.BackendTLSPolicy) ir.Source {
	return translate.Source(ir.KindBackendTLSPolicy, p)
}

func (t *translator) indexBackendTLS() *backendTLSIndex {
	idx := &backendTLSIndex{
		byTarget: make(map[string]*gwapiv1.BackendTLSPolicy),
		lowered:  make(map[string]*ir.BackendTLS),
		failed:   make(map[string]string),
	}
	for _, p := range translate.ByAge(t.cfg.Cache.BackendTLSPolicies()) {
		src := policySource(p)
		for _, ref := range p.Spec.TargetRefs {
			if string(ref.Group) != "" || string(ref.Kind) != kindService {
				t.reject(src, "targetRef kind %s/%s is not supported", ref.Group, ref.Kind)
				continue
			}
			var section string
			if ref.SectionName != nil {
				section = string(*ref.SectionName)
			}
			key := targetKey(p.Namespace, string(ref.Name), section)
			if owner, taken := idx.byTarget[key]; taken {
				t.reject(src, "targetRef %s is already selected by the older policy %s",
					key, policySource(owner).Key())
				continue
			}
			idx.byTarget[key] = p
		}
	}
	return idx
}

func targetKey(namespace, service, section string) string {
	return namespace + "/" + service + "/" + section
}

func (t *translator) backendTLS(namespace, service, port string) (*ir.BackendTLS, string, bool) {
	// A port-specific policy outranks one for the whole Service.
	idx := t.tlsPolicies
	if idx == nil {
		return nil, "", false
	}
	p, ok := idx.byTarget[targetKey(namespace, service, port)]
	if !ok {
		if p, ok = idx.byTarget[targetKey(namespace, service, "")]; !ok {
			return nil, "", false
		}
	}
	key := policySource(p).Key()
	if tls, done := idx.lowered[key]; done {
		return tls, "", true
	}
	if reason, done := idx.failed[key]; done {
		return nil, reason, true
	}
	tls, err := t.lowerBackendTLS(p)
	if err != nil {
		reason := fmt.Sprintf("%s: %s", key, err)
		idx.failed[key] = reason
		return nil, reason, true
	}
	idx.lowered[key] = tls
	return tls, "", true
}

func (t *translator) lowerBackendTLS(p *gwapiv1.BackendTLSPolicy) (*ir.BackendTLS, error) {
	// a reference that cannot be resolved fails the policy: the spec requires connections
	// under an invalid reference to fail rather than proceed unverified
	v := p.Spec.Validation
	hostname, err := translate.Hostname(string(v.Hostname), translate.HostnamePrecise)
	if err != nil {
		return nil, err
	}
	if len(v.SubjectAltNames) > 0 {
		return nil, errTLSSubjectAltName
	}
	if len(p.Spec.Options) > 0 {
		t.reject(policySource(p), "options are not supported and are ignored")
	}
	wellKnown := v.WellKnownCACertificates != nil && *v.WellKnownCACertificates != ""
	switch {
	case len(v.CACertificateRefs) > 0 && wellKnown:
		return nil, errTLSBothValidation
	case len(v.CACertificateRefs) == 0 && !wellKnown:
		return nil, errTLSNoValidation
	case wellKnown:
		if string(*v.WellKnownCACertificates) != wellKnownSystemCAs {
			return nil, fmt.Errorf("%w: %q", errTLSWellKnown, *v.WellKnownCACertificates)
		}
		return &ir.BackendTLS{Hostname: hostname, System: true}, nil
	}
	out := &ir.BackendTLS{Hostname: hostname}
	for _, ref := range v.CACertificateRefs {
		pem, err := t.caBundle(p.Namespace, ref)
		if err != nil {
			return nil, err
		}
		out.CACertificates = append(out.CACertificates, pem)
	}
	return out, nil
}

func (t *translator) caBundle(namespace string, ref gwapiv1.LocalObjectReference) (string, error) {
	name := string(ref.Name)
	var data []byte
	var found bool
	switch {
	case string(ref.Group) != "":
		return "", fmt.Errorf("%w: %s/%s", errTLSCARefKind, ref.Group, ref.Kind)
	case string(ref.Kind) == kindConfigMap:
		if cm := t.cfg.Cache.ConfigMap(namespace, name); cm != nil {
			found = true
			if s, ok := cm.Data[caCertificateKey]; ok {
				data = []byte(s)
			} else {
				data = cm.BinaryData[caCertificateKey]
			}
		}
	case string(ref.Kind) == kindSecret:
		if sec := t.cfg.Cache.Secret(namespace, name); sec != nil {
			found = true
			data = sec.Data[caCertificateKey]
		}
	default:
		return "", fmt.Errorf("%w: %s", errTLSCARefKind, ref.Kind)
	}
	if !found {
		return "", fmt.Errorf("%w: %s %s/%s", errTLSCARefMissing, ref.Kind, namespace, name)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("%w: %s %s/%s", errTLSCARefKey, ref.Kind, namespace, name)
	}
	if _, err := tlso.ValidateCABundle(data); err != nil {
		return "", fmt.Errorf("%w: %s %s/%s", errTLSCARefPEM, ref.Kind, namespace, name)
	}
	return strings.TrimSpace(string(data)), nil
}
