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
	"slices"
	"strconv"
	"strings"

	awsopts "github.com/trickstercache/trickster/v2/pkg/discovery/aws/options"
	"github.com/trickstercache/trickster/v2/pkg/discovery/providers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
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
	// Filter is a provider-native, server-side selection expression. It is
	// passed through rather than interpreted, so its syntax is the
	// provider's own -- for consul, a filter expression such as
	// 'Service.Meta.version == "2"'.
	Filter string `yaml:"filter,omitempty"`
	// Tags selects only members carrying every listed tag, for providers
	// whose catalog has a native tag concept. It is the friendly form of
	// the common case that Filter can also express.
	Tags []string `yaml:"tags,omitempty"`
	// Filters is the name/values form of provider-native selection, for
	// APIs whose filters are structured rather than an expression. It is a
	// separate field from Filter because the two are genuinely different
	// syntaxes, not two spellings of one: EC2 takes Filter.N.Name with
	// repeated values, where consul takes an expression string.
	Filters map[string][]string `yaml:"filters,omitempty"`
	// AddressType selects which of a cloud instance's addresses becomes the
	// member address: private (default), public, or ipv6. Cloud inventories
	// return hosts with several addresses and no opinion about which one a
	// caller should reach.
	AddressType string `yaml:"address_type,omitempty"`
	// PortLabel names a key in the provider's own metadata namespace (an
	// EC2 tag, an ECS task tag, a GCE label) whose value is the member's
	// port. Cloud APIs return hosts, not endpoints, so a port has to come
	// from somewhere: either Port, statically, or from the instances
	// themselves through this.
	PortLabel string `yaml:"port_label,omitempty"`
	// Cluster names the cluster to query for providers whose API is scoped
	// by one, such as ECS. When empty, the provider's own default applies.
	Cluster string `yaml:"cluster,omitempty"`
}

// Clone returns a perfect copy of the Query
func (q *Query) Clone() *Query {
	out := *q
	if q.Selector != nil {
		out.Selector = maps.Clone(q.Selector)
	}
	if q.Tags != nil {
		out.Tags = slices.Clone(q.Tags)
	}
	if q.Filters != nil {
		out.Filters = make(map[string][]string, len(q.Filters))
		for k, v := range q.Filters {
			out.Filters[k] = slices.Clone(v)
		}
	}
	return &out
}

// Query field names as they appear in config. They are constants because
// each is named twice -- once in queryFields, once in every provider's
// accepted set -- and a typo in the second place would silently make the
// field unacceptable everywhere.
const (
	fieldKind              = "kind"
	fieldNamespace         = "namespace"
	fieldService           = "service"
	fieldSelector          = "selector"
	fieldPort              = "port"
	fieldReplicaGroupLabel = "replica_group_label"
	fieldSRVName           = "srv_name"
	fieldHostname          = "hostname"
	fieldPath              = "path"
	fieldScheme            = "scheme"
	fieldFilter            = "filter"
	fieldTags              = "tags"
	fieldFilters           = "filters"
	fieldAddressType       = "address_type"
	fieldPortLabel         = "port_label"
	fieldCluster           = "cluster"
)

// Address types accepted by Query.AddressType
const (
	// AddressPrivate selects an instance's private address (the default)
	AddressPrivate = "private"
	// AddressPublic selects an instance's public address
	AddressPublic = "public"
	// AddressIPv6 selects an instance's IPv6 address
	AddressIPv6 = "ipv6"
)

// addressTypes enumerates the valid AddressType values
var addressTypes = sets.New([]string{AddressPrivate, AddressPublic, AddressIPv6})

// queryField pairs a Query field's config name with a reader, so that
// validation can ask "is this set" without reflection.
type queryField struct {
	name  string
	isSet func(*Query) bool
}

// queryFields enumerates every Query field. Validation is expressed as
// "which fields does this provider accept" rather than, per provider,
// "which of the other providers' fields must be unset". The latter is
// O(providers x fields) to maintain, and its failure mode is silent: a
// field added without touching every other provider's validator becomes
// quietly legal everywhere. With a roster of providers still to land, the
// table is the difference between a one-line change and eight.
var queryFields = []queryField{
	{fieldKind, func(q *Query) bool { return q.Kind != "" }},
	{fieldNamespace, func(q *Query) bool { return q.Namespace != "" }},
	{fieldService, func(q *Query) bool { return q.Service != "" }},
	{fieldSelector, func(q *Query) bool { return len(q.Selector) > 0 }},
	{fieldPort, func(q *Query) bool { return q.Port != "" }},
	{fieldReplicaGroupLabel, func(q *Query) bool { return q.ReplicaGroupLabel != "" }},
	{fieldSRVName, func(q *Query) bool { return q.SRVName != "" }},
	{fieldHostname, func(q *Query) bool { return q.Hostname != "" }},
	{fieldPath, func(q *Query) bool { return q.Path != "" }},
	{fieldScheme, func(q *Query) bool { return q.Scheme != "" }},
	{fieldFilter, func(q *Query) bool { return q.Filter != "" }},
	{fieldTags, func(q *Query) bool { return len(q.Tags) > 0 }},
	{fieldFilters, func(q *Query) bool { return len(q.Filters) > 0 }},
	{fieldAddressType, func(q *Query) bool { return q.AddressType != "" }},
	{fieldPortLabel, func(q *Query) bool { return q.PortLabel != "" }},
	{fieldCluster, func(q *Query) bool { return q.Cluster != "" }},
}

// providerQueryFields names the query fields each provider accepts. A field
// set but not listed here for the discoverer's provider fails startup
// rather than being silently ignored. New providers register their
// accepted fields here; new fields are added to queryFields above and to
// the accepting providers here.
var providerQueryFields = map[string]sets.Set[string]{
	providers.Kubernetes: sets.New([]string{
		fieldKind, fieldNamespace, fieldService, fieldSelector, fieldPort,
		fieldReplicaGroupLabel, fieldScheme,
	}),
	providers.DNSSRV: sets.New([]string{fieldSRVName, fieldScheme}),
	providers.DNSA:   sets.New([]string{fieldHostname, fieldPort, fieldScheme}),
	providers.File:   sets.New([]string{fieldPath}),
	providers.HTTPSD: sets.New([]string{fieldPath, fieldScheme}),
	// gcp selects with a server-side filter expression and network tags,
	// and like ec2 needs a port and an address choice, since a compute
	// instance is a host rather than an endpoint
	providers.GCP: sets.New([]string{
		fieldFilter, fieldTags, fieldPort, fieldPortLabel,
		fieldAddressType, fieldScheme, fieldReplicaGroupLabel,
	}),
	providers.Consul: sets.New([]string{
		fieldService, fieldTags, fieldFilter, fieldScheme,
		fieldReplicaGroupLabel,
	}),
	// nomad's native registry conveys no per-instance health, so there is
	// no readiness to filter on; tags narrow client-side, filter narrows
	// server-side
	providers.Nomad: sets.New([]string{
		fieldService, fieldTags, fieldFilter, fieldScheme,
	}),
	// the aws provider accepts the union of its services' fields; which are
	// meaningful for a given aws.service is checked in validateAWS, which
	// can see the discoverer's options
	providers.AWS: sets.New([]string{
		fieldFilters, fieldTags, fieldPort, fieldPortLabel,
		fieldAddressType, fieldScheme, fieldReplicaGroupLabel,
		fieldService, fieldCluster,
	}),
}

// Validate validates the Query against the discoverer it will be submitted
// to, applying provider defaults (e.g., kubernetes kind) to unset fields.
// albName is used for error context.
//
// It takes the whole discoverer Options rather than just a provider name
// because which query fields are legal is not always a function of the
// provider alone: the forthcoming 'aws' provider accepts different fields
// per aws.service, and that lives on the Options.
func (q *Query) Validate(albName string, o *Options) error {
	if o == nil {
		return NewErrInvalidQuery(albName, "no discoverer options provided")
	}
	if q.Scheme != "" && q.Scheme != SchemeHTTP && q.Scheme != SchemeHTTPS {
		return NewErrInvalidQuery(albName, "'scheme' must be http or https")
	}
	accepted, ok := providerQueryFields[o.Provider]
	if !ok {
		return NewErrInvalidQuery(albName, "unknown discovery provider "+o.Provider)
	}
	for _, f := range queryFields {
		if f.isSet(q) && !accepted.Contains(f.name) {
			return NewErrInvalidQueryField(albName, f.name, o.Provider)
		}
	}
	switch o.Provider {
	case providers.Kubernetes:
		return q.validateKubernetes(albName)
	case providers.DNSSRV:
		return q.validateDNSSRV(albName)
	case providers.DNSA:
		return q.validateDNSA(albName)
	case providers.File:
		return q.validateFile(albName)
	case providers.HTTPSD:
		return q.validateHTTPSD(albName)
	case providers.Consul:
		return q.validateConsul(albName)
	case providers.Nomad:
		return q.validateNomad(albName)
	case providers.AWS:
		return q.validateAWS(albName, o)
	case providers.GCP:
		return q.validateGCP(albName)
	}
	return NewErrInvalidQuery(albName, "unknown discovery provider "+o.Provider)
}

// validateKubernetes applies the rules the accepted-field table cannot
// express: the kind default, and which fields each kind requires.
func (q *Query) validateKubernetes(albName string) error {
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
	if q.SRVName == "" {
		return NewErrInvalidQuery(albName,
			"'srv_name' is required for the dns_srv provider")
	}
	return nil
}

func (q *Query) validateDNSA(albName string) error {
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
	if q.Path == "" {
		return NewErrInvalidQuery(albName,
			"'path' is required for the file provider")
	}
	return nil
}

// validateHTTPSD checks the optional URL path. Unlike the file provider's
// required filesystem path, this one defaults to the endpoint itself.
func (q *Query) validateHTTPSD(albName string) error {
	if q.Path != "" && !strings.HasPrefix(q.Path, "/") {
		return NewErrInvalidQuery(albName,
			"'path' must begin with '/' for the http_sd provider")
	}
	return nil
}

// validateConsul checks the consul query. The service name is the only
// required field; tags and filter narrow it server-side.
func (q *Query) validateConsul(albName string) error {
	if q.Service == "" {
		return NewErrInvalidQuery(albName,
			"'service' is required for the consul provider")
	}
	if slices.Contains(q.Tags, "") {
		return NewErrInvalidQuery(albName,
			"'tags' entries cannot be empty")
	}
	return nil
}

// validateNomad checks the nomad query, whose only required field is the
// registered service name.
func (q *Query) validateNomad(albName string) error {
	if q.Service == "" {
		return NewErrInvalidQuery(albName,
			"'service' is required for the nomad provider")
	}
	if slices.Contains(q.Tags, "") {
		return NewErrInvalidQuery(albName, "'tags' entries cannot be empty")
	}
	return nil
}

// validateAWS checks the aws query. A cloud instance inventory returns
// hosts rather than endpoints, so a port must come from somewhere: either
// statically, or from each instance's own metadata.
func (q *Query) validateAWS(albName string, o *Options) error {
	// which fields are meaningful depends on the aws.service, which is why
	// Query.Validate takes the whole Options rather than a provider name. An
	// unset service applies no per-service rules here; Options.Validate
	// reports the missing service itself, with a better message than any
	// field-level complaint would give.
	switch o.AWS.GetService() {
	case awsopts.ServiceEC2:
		if q.Service != "" {
			return NewErrInvalidQueryField(albName, fieldService,
				providers.AWS+" service "+awsopts.ServiceEC2)
		}
		if q.Cluster != "" {
			return NewErrInvalidQueryField(albName, fieldCluster,
				providers.AWS+" service "+awsopts.ServiceEC2)
		}
	case awsopts.ServiceECS:
		// ECS selects by cluster and service, not by instance attributes
		for _, f := range []struct {
			name string
			set  bool
		}{
			{fieldFilters, len(q.Filters) > 0},
			{fieldAddressType, q.AddressType != ""},
		} {
			if f.set {
				return NewErrInvalidQueryField(albName, f.name,
					providers.AWS+" service "+awsopts.ServiceECS)
			}
		}
	}
	if q.AddressType != "" && !addressTypes.Contains(q.AddressType) {
		return NewErrInvalidQuery(albName,
			"'address_type' must be private, public or ipv6")
	}
	if q.Port == "" && q.PortLabel == "" {
		return NewErrInvalidQuery(albName,
			"one of 'port' or 'port_label' is required for the aws provider, "+
				"because instances have addresses but no port")
	}
	if q.Port != "" && !validPortNumber(q.Port) {
		return NewErrInvalidQuery(albName,
			"'port' must be a port number between 1 and 65535")
	}
	for name, values := range q.Filters {
		if name == "" {
			return NewErrInvalidQuery(albName, "'filters' names cannot be empty")
		}
		if len(values) == 0 {
			return NewErrInvalidQuery(albName,
				"'filters' entry "+name+" has no values")
		}
	}
	if slices.Contains(q.Tags, "") {
		return NewErrInvalidQuery(albName, "'tags' entries cannot be empty")
	}
	return nil
}

// validateGCP checks the gcp query. Like ec2, a compute instance is a host
// rather than an endpoint, so a port must come from somewhere.
func (q *Query) validateGCP(albName string) error {
	if q.AddressType != "" && !addressTypes.Contains(q.AddressType) {
		return NewErrInvalidQuery(albName,
			"'address_type' must be private, public or ipv6")
	}
	if q.Port == "" && q.PortLabel == "" {
		return NewErrInvalidQuery(albName,
			"one of 'port' or 'port_label' is required for the gcp provider, "+
				"because instances have addresses but no port")
	}
	if q.Port != "" && !validPortNumber(q.Port) {
		return NewErrInvalidQuery(albName,
			"'port' must be a port number between 1 and 65535")
	}
	if slices.Contains(q.Tags, "") {
		return NewErrInvalidQuery(albName, "'tags' entries cannot be empty")
	}
	return nil
}

// GetAddressType returns the configured address type, or the default.
func (q *Query) GetAddressType() string {
	if q.AddressType == "" {
		return AddressPrivate
	}
	return q.AddressType
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
