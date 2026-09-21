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

// Package cachepolicy is the TricksterCachePolicy custom resource: its Go shape, how it is read
// through the dynamic client, and the index that decides which policy governs each route target
package cachepolicy

import (
	"maps"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/kube/gateway/ir"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// The resource's identity in the API
const (
	Group    = appinfo.Domain
	Version  = "v1alpha1"
	Kind     = ir.KindCachePolicy
	Resource = "trickstercachepolicies"
)

var (
	// GroupVersion is the API group version the resource is served under
	GroupVersion = schema.GroupVersion{Group: Group, Version: Version}
	// GVR is the resource the dynamic client reads and writes
	GVR = GroupVersion.WithResource(Resource)
	// GVK is the kind the resource carries
	GVK = GroupVersion.WithKind(Kind)
)

// Target kinds a policy may name; a Service target governs every backendRef naming the Service
const (
	KindGateway   = ir.KindGateway
	KindHTTPRoute = ir.KindHTTPRoute
	KindService   = "Service"
	KindIngress   = ir.KindIngress
)

// Result header dispositions, as the resource spells them
const (
	ResultHeaderExpose = "Expose"
	ResultHeaderHide   = "Hide"
)

// CachePolicy is a TricksterCachePolicy object
type CachePolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              Spec                 `json:"spec"`
	Status            gwapiv1.PolicyStatus `json:"status"`
}

// TargetRef names one object in the policy's own namespace that the policy governs
type TargetRef struct {
	// Group is the target's API group; empty is the kind's own group
	Group string `json:"group,omitempty"`
	// Kind is Gateway, HTTPRoute, Service or Ingress
	Kind string `json:"kind"`
	// Name is the target's name
	Name string `json:"name"`
	// SectionName narrows an HTTPRoute target to one named rule, or a
	// Service target to one named port
	SectionName string `json:"sectionName,omitempty"`
}

// Spec is what a policy sets on the routes it governs; every field is optional and an unset
// one leaves the less specific policy's, or the configured default, in force
type Spec struct {
	TargetRefs []TargetRef `json:"targetRefs"`
	// Handler is proxy or proxycache
	Handler string `json:"handler,omitempty"`
	// Provider makes the generated backend a time series provider whose own API paths it
	// accelerates: prometheus, influxdb, clickhouse or graphite
	Provider string `json:"provider,omitempty"`
	// CacheName and NegativeCacheName name a configured cache and negative cache
	CacheName         string `json:"cacheName,omitempty"`
	NegativeCacheName string `json:"negativeCacheName,omitempty"`
	// MaxTTL caps how long a cached object is served before revalidation, and Timeout bounds
	// the upstream request; both are durations with a unit
	MaxTTL  string `json:"maxTTL,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	// CollapsedForwarding is basic or progressive
	CollapsedForwarding string `json:"collapsedForwarding,omitempty"`
	// CacheKeyParams and CacheKeyHeaders are hashed into the cache key; an omitted list inherits
	// a less specific policy's and an empty one clears it, so the two are kept apart
	CacheKeyParams  []string `json:"cacheKeyParams"`
	CacheKeyHeaders []string `json:"cacheKeyHeaders"`
	// RequestHeaders and ResponseHeaders are header updates; a name prefixed with '-' deletes
	// the header and one prefixed with '+' appends to it
	RequestHeaders  map[string]string `json:"requestHeaders,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	// CORS is the CORS response header policy
	CORS *CORS `json:"cors,omitempty"`
	// HealthMode is probe or provider, for the endpoint routing mode
	HealthMode string `json:"healthMode,omitempty"`
	// LoadBalancing is rr, p2c, lc, lt or hrw: how traffic is spread across a Service's
	// endpoints in the endpoint routing mode
	LoadBalancing string `json:"loadBalancing,omitempty"`
	// LoadBalancingKey is what hrw keeps together: client_ip, host, sni, header:<name>,
	// cookie:<name> or query:<name>
	LoadBalancingKey string `json:"loadBalancingKey,omitempty"`
	// ResultHeader is Expose or Hide: whether X-Trickster-Result reaches the client
	ResultHeader string `json:"resultHeader,omitempty"`
}

// CORS is how origin CORS headers are combined with configured ones
type CORS struct {
	// Mode is preserve, merge, replace or disable
	Mode string `json:"mode,omitempty"`
	// Headers are the CORS response headers merge and replace apply
	Headers map[string]string `json:"headers,omitempty"`
}

// DeepCopy returns a deep copy of the policy
func (p *CachePolicy) DeepCopy() *CachePolicy {
	if p == nil {
		return nil
	}
	out := &CachePolicy{TypeMeta: p.TypeMeta}
	p.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = p.Spec.clone()
	p.Status.DeepCopyInto(&out.Status)
	return out
}

// DeepCopyObject returns a deep copy as a runtime.Object
func (p *CachePolicy) DeepCopyObject() runtime.Object {
	return p.DeepCopy()
}

func (s Spec) clone() Spec {
	s.TargetRefs = slices.Clone(s.TargetRefs)
	s.CacheKeyParams = slices.Clone(s.CacheKeyParams)
	s.CacheKeyHeaders = slices.Clone(s.CacheKeyHeaders)
	s.RequestHeaders = maps.Clone(s.RequestHeaders)
	s.ResponseHeaders = maps.Clone(s.ResponseHeaders)
	if s.CORS != nil {
		s.CORS = &CORS{Mode: s.CORS.Mode, Headers: maps.Clone(s.CORS.Headers)}
	}
	return s
}

// FromUnstructured reads a policy from the object the dynamic client delivers
func FromUnstructured(u *unstructured.Unstructured) (*CachePolicy, error) {
	out := &CachePolicy{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
		u.UnstructuredContent(), out); err != nil {
		return nil, err
	}
	return out, nil
}

// ToUnstructured renders a policy as the object the dynamic client writes
func ToUnstructured(p *CachePolicy) (*unstructured.Unstructured, error) {
	p.APIVersion, p.Kind = GroupVersion.String(), Kind
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(p)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: content}, nil
}
