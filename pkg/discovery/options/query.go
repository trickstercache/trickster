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

package options

import (
	"maps"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
)

// Kubernetes query kinds
const (
	// KindEndpointSlices selects the ready endpoint addresses of a named
	// Service via its EndpointSlices (the default kubernetes query kind)
	KindEndpointSlices = "endpointslices"
	// KindService selects the ClusterIP of each Service matching a selector
	KindService = "service"
	// KindPods selects the pod IPs of Pods matching a label selector
	KindPods = "pods"
)

// Scheme names accepted by Query.Scheme
const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// Query defines what an ALB selects from a discoverer. The meaningful
// fields depend on the referenced discoverer's provider; Validate enforces
// per-provider field usage so that misplaced fields fail startup rather
// than being silently ignored.
type Query struct {
	// Kind is the kubernetes query kind: endpointslices (default), service,
	// or pods
	Kind string `yaml:"kind,omitempty"`
	// Namespace is the kubernetes namespace to query. When empty, the
	// provider queries the client's default namespace (in-cluster: the
	// pod's own namespace).
	Namespace string `yaml:"namespace,omitempty"`
	// Service is the target Service name for the endpointslices kind
	Service string `yaml:"service,omitempty"`
	// Selector is the label selector for the service and pods kinds, and an
	// optional additional filter for endpointslices
	Selector map[string]string `yaml:"selector,omitempty"`
	// Port selects the member port: for kubernetes, a named port or port
	// number (default: the sole port, or error on ambiguity); for dns_a, a
	// required port number
	Port string `yaml:"port,omitempty"`
	// ReplicaGroupLabel names a kubernetes label whose value assigns each
	// discovered member to a TSM replica group: read from the Pod for the
	// pods kind (and for endpointslices, via the endpoint's target pod),
	// or from the Service for the service kind. Members without the label
	// fall back to the template backend's replica_group semantics.
	ReplicaGroupLabel string `yaml:"replica_group_label,omitempty"`
	// SRVName is the SRV record name for the dns_srv provider
	// (e.g. _prometheus._tcp.example.com)
	SRVName string `yaml:"srv_name,omitempty"`
	// Hostname is the name whose A/AAAA records are resolved by the dns_a
	// provider
	Hostname string `yaml:"hostname,omitempty"`
	// Path is the member-list file path for the file provider
	Path string `yaml:"path,omitempty"`
	// Scheme is the scheme (http, https) applied to discovered members for
	// providers that do not convey one (default http). Ignored by the file
	// provider, whose entries carry their own scheme.
	Scheme string `yaml:"scheme,omitempty"`
}

// Clone returns a perfect copy of the Query
func (q *Query) Clone() *Query {
	out := *q
	if q.Selector != nil {
		out.Selector = maps.Clone(q.Selector)
	}
	return &out
}

// Validate validates the Query against the provider of the discoverer it
// will be submitted to, applying provider defaults (e.g., kubernetes kind)
// to unset fields. albName is used for error context.
func (q *Query) Validate(albName, provider string) error {
	if q.Scheme != "" && q.Scheme != SchemeHTTP && q.Scheme != SchemeHTTPS {
		return NewErrInvalidQuery(albName, "'scheme' must be http or https")
	}
	switch provider {
	case providers.Kubernetes:
		return q.validateKubernetes(albName)
	case providers.DNSSRV:
		return q.validateDNSSRV(albName)
	case providers.DNSA:
		return q.validateDNSA(albName)
	case providers.File:
		return q.validateFile(albName)
	}
	return NewErrInvalidQuery(albName, "unknown discovery provider "+provider)
}

func (q *Query) validateKubernetes(albName string) error {
	if err := q.requireUnset(albName, providers.Kubernetes,
		field{"srv_name", q.SRVName}, field{"hostname", q.Hostname},
		field{"path", q.Path}); err != nil {
		return err
	}
	if q.Kind == "" {
		q.Kind = KindEndpointSlices
	}
	switch q.Kind {
	case KindEndpointSlices:
		if q.Service == "" {
			return NewErrInvalidQuery(albName,
				"'service' is required for the endpointslices kind")
		}
	case KindService, KindPods:
		if q.Service != "" && q.Kind == KindPods {
			return NewErrInvalidQuery(albName,
				"'service' is not valid for the pods kind")
		}
		if len(q.Selector) == 0 && q.Service == "" {
			return NewErrInvalidQuery(albName,
				"a 'selector' is required for the "+q.Kind+" kind")
		}
	default:
		return NewErrInvalidQuery(albName,
			"'kind' must be endpointslices, service or pods")
	}
	if q.Port != "" && !validPortName(q.Port) {
		return NewErrInvalidQuery(albName,
			"'port' must be a port name or number")
	}
	return nil
}

func (q *Query) validateDNSSRV(albName string) error {
	if err := q.requireUnset(albName, providers.DNSSRV,
		field{"kind", q.Kind}, field{"namespace", q.Namespace},
		field{"service", q.Service}, field{"hostname", q.Hostname},
		field{"path", q.Path}, field{"port", q.Port},
		field{"replica_group_label", q.ReplicaGroupLabel}); err != nil {
		return err
	}
	if len(q.Selector) > 0 {
		return NewErrInvalidQueryField(albName, "selector", providers.DNSSRV)
	}
	if q.SRVName == "" {
		return NewErrInvalidQuery(albName,
			"'srv_name' is required for the dns_srv provider")
	}
	return nil
}

func (q *Query) validateDNSA(albName string) error {
	if err := q.requireUnset(albName, providers.DNSA,
		field{"kind", q.Kind}, field{"namespace", q.Namespace},
		field{"service", q.Service}, field{"srv_name", q.SRVName},
		field{"path", q.Path},
		field{"replica_group_label", q.ReplicaGroupLabel}); err != nil {
		return err
	}
	if len(q.Selector) > 0 {
		return NewErrInvalidQueryField(albName, "selector", providers.DNSA)
	}
	if q.Hostname == "" {
		return NewErrInvalidQuery(albName,
			"'hostname' is required for the dns_a provider")
	}
	if q.Port == "" {
		return NewErrInvalidQuery(albName,
			"'port' is required for the dns_a provider")
	}
	if !validPortNumber(q.Port) {
		return NewErrInvalidQuery(albName,
			"'port' must be a port number between 1 and 65535")
	}
	return nil
}

func (q *Query) validateFile(albName string) error {
	if err := q.requireUnset(albName, providers.File,
		field{"kind", q.Kind}, field{"namespace", q.Namespace},
		field{"service", q.Service}, field{"srv_name", q.SRVName},
		field{"hostname", q.Hostname}, field{"port", q.Port},
		field{"scheme", q.Scheme},
		field{"replica_group_label", q.ReplicaGroupLabel}); err != nil {
		return err
	}
	if len(q.Selector) > 0 {
		return NewErrInvalidQueryField(albName, "selector", providers.File)
	}
	if q.Path == "" {
		return NewErrInvalidQuery(albName,
			"'path' is required for the file provider")
	}
	return nil
}

type field struct {
	name  string
	value string
}

func (q *Query) requireUnset(albName, provider string, fields ...field) error {
	for _, f := range fields {
		if f.value != "" {
			return NewErrInvalidQueryField(albName, f.name, provider)
		}
	}
	return nil
}

func validPortNumber(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n > 0 && n <= 65535
}

// validPortName reports whether s is usable as a kubernetes port reference:
// either a valid port number or an IANA_SVC_NAME-style port name
func validPortName(s string) bool {
	if s == "" {
		return false
	}
	if _, err := strconv.Atoi(s); err == nil {
		return validPortNumber(s)
	}
	if len(s) > 15 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
