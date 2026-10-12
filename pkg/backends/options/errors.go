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
	"errors"
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// ErrInvalidMetadata is an error for invalid metadata
var ErrInvalidMetadata = errors.New("invalid options metadata")

// ErrInvalidMaxShardSizeTime is an error for when 'shard_max_size_time' is not
// a multiple 'shard_step'
var ErrInvalidMaxShardSizeTime = errors.New(
	"'shard_max_size_time' must be a multiple of 'shard_step' when both are non-zero")

// ErrInvalidMaxShardSize is an error for when both 'shard_max_size_time' and
// 'shard_max_size_points' are used on the same backend
var ErrInvalidMaxShardSize = errors.New(
	"'shard_max_size_time' and 'shard_max_size_points' cannot both be non-zero")

// ErrStepAlignmentWithFastForwardDisable is an error for a backend that sets both step_alignment
// and the fast_forward_disable key it supersedes
var ErrStepAlignmentWithFastForwardDisable = errors.New(
	"'step_alignment' and 'fast_forward_disable' cannot both be set; remove 'fast_forward_disable'")

// ErrVolatileWindowWithBackfillTolerance is an error for a backend that sets both volatile_window
// and backfill_tolerance
var ErrVolatileWindowWithBackfillTolerance = errors.New(
	"'volatile_window' and 'backfill_tolerance' cannot both be set; remove 'backfill_tolerance'")

// ErrVolatileWindowPointsWithBackfillTolerancePoints is an error for a backend that sets both
// volatile_window_points and backfill_tolerance_points
var ErrVolatileWindowPointsWithBackfillTolerancePoints = errors.New(
	"'volatile_window_points' and 'backfill_tolerance_points' cannot both be set; remove 'backfill_tolerance_points'")

// ErrSRVRequiresHTTP is an error for origin_resolution mode srv on a non-HTTP origin_url
var ErrSRVRequiresHTTP = errors.New("'origin_resolution.mode: srv' requires an http:// or https:// origin_url")

// ErrSRVWithIPOrigin is an error for origin_resolution mode srv with an IP literal origin_url host
var ErrSRVWithIPOrigin = errors.New(
	"'origin_resolution.mode: srv' requires an origin_url host that is an SRV owner name, not an IP address")

// ErrSRVTargetWithServerName is an error for origin_resolution.tls_server_name target with a tls.server_name
var ErrSRVTargetWithServerName = errors.New(
	"'origin_resolution.tls_server_name: target' and 'tls.server_name' cannot both be set")

// ErrFlavorProvider is an error for a prometheus.flavor on a backend whose provider is not prometheus
var ErrFlavorProvider = errors.New("'prometheus.flavor' requires provider 'prometheus'")

// ErrFlavorMissingOrigin is an error for a cloudwatch flavor with neither origin_url nor sigv4.region
var ErrFlavorMissingOrigin = errors.New(
	"the cloudwatch flavor requires 'origin_url', or 'sigv4.region' to derive it from")

// ErrFlavorRegionMismatch is an error for an AWS origin_url whose region differs from sigv4.region
var ErrFlavorRegionMismatch = errors.New("'origin_url' and 'sigv4.region' name different regions")

// ErrUnsupportedStepAlignment is an error for a step_alignment the backend's provider doesn't support
var ErrUnsupportedStepAlignment = errors.New("unsupported step_alignment")

// NewErrInvalidStepAlignment returns an error for a step_alignment value that isn't exactly one mode
func NewErrInvalidStepAlignment(value timeseries.StepAlignment, backendName string) error {
	return fmt.Errorf(`%w for backend "%s": %#x is not exactly one mode`,
		timeseries.ErrInvalidStepAlignment, backendName, uint8(value))
}

// NewErrUnsupportedStepAlignment returns an error naming the modes the backend's provider supports
func NewErrUnsupportedStepAlignment(mode, supported timeseries.StepAlignment, provider,
	backendName string,
) error {
	names := supported.String()
	if names == "" {
		names = "no step alignment modes"
	}
	return fmt.Errorf(`%w "%s" for backend "%s": provider "%s" supports %s`,
		ErrUnsupportedStepAlignment, mode, backendName, provider, names)
}

// NewErrStepAlignmentUnsupportedByMembers returns an error naming the pool members of an ALB that
// can't apply the mode it applies to every member
func NewErrStepAlignmentUnsupportedByMembers(mode timeseries.StepAlignment, albName string,
	members []string,
) error {
	return fmt.Errorf(`%w "%s" for alb "%s": pool members [%s] can't apply it`,
		ErrUnsupportedStepAlignment, mode, albName, strings.Join(members, ", "))
}

// ErrMissingProvider is an error type for missing provider
type ErrMissingProvider struct {
	error
}

// NewErrMissingProvider returns a new missing provider error
func NewErrMissingProvider(backendName string) error {
	return &ErrMissingProvider{
		error: fmt.Errorf(`missing provider for backend "%s"`, backendName),
	}
}

// ErrMissingStaticOptions is an error type for a static backend with no static block
type ErrMissingStaticOptions struct {
	error
}

// NewErrMissingStaticOptions returns a new missing static options error
func NewErrMissingStaticOptions(backendName string) error {
	return &ErrMissingStaticOptions{
		error: fmt.Errorf(`missing static options for backend "%s"`, backendName),
	}
}

// ErrUnsupportedOption is an error type for an option the backend's provider can't honor
type ErrUnsupportedOption struct {
	error
}

// NewErrUnsupportedOption returns a new unsupported option error
func NewErrUnsupportedOption(option, provider, backendName string) error {
	return &ErrUnsupportedOption{
		error: fmt.Errorf(`option "%s" is not supported by provider "%s" for backend "%s"`,
			option, provider, backendName),
	}
}

// ErrMissingOriginURL is an error type for missing origin URL
type ErrMissingOriginURL struct {
	error
}

// NewErrMissingOriginURL returns a new missing origin URL error
func NewErrMissingOriginURL(backendName string) error {
	return &ErrMissingOriginURL{
		error: fmt.Errorf(`missing origin-url for backend "%s"`, backendName),
	}
}

// ErrInvalidNegativeCacheName is an error type for invalid negative cache name
type ErrInvalidNegativeCacheName struct {
	error
}

// NewErrInvalidNegativeCacheName returns a new invalid negative cache name error
func NewErrInvalidNegativeCacheName(cacheName string) error {
	return &ErrInvalidNegativeCacheName{
		error: fmt.Errorf(`invalid negative_cache name: %s`, cacheName),
	}
}

// ErrInvalidRuleName is an error type for invalid rule name
type ErrInvalidRuleName struct {
	error
}

// NewErrInvalidRuleName returns a new invalid rule name error
func NewErrInvalidRuleName(ruleName, backendName string) error {
	return &ErrInvalidRuleName{
		error: fmt.Errorf(`invalid rule name "%s" provided in backend options "%s"`,
			ruleName, backendName),
	}
}

// ErrInvalidCacheName is an error type for invalid cache name
type ErrInvalidCacheName struct {
	error
}

// NewErrInvalidCacheName returns a new invalid cache name error
func NewErrInvalidCacheName(cacheName, backendName string) error {
	return &ErrInvalidCacheName{
		error: fmt.Errorf(`invalid cache_name "%s" provided in backend options "%s"`,
			cacheName, backendName),
	}
}

// ErrInvalidAuthenticatorName is an error type for invalid cache name
type ErrInvalidAuthenticatorName struct {
	error
}

// ErrInvalidIPACLName is an error type for an ip_acl_name that is not defined.
type ErrInvalidIPACLName struct {
	error
}

// NewErrInvalidIPACLName returns a new invalid access-list name error.
func NewErrInvalidIPACLName(aclName, backendName string) error {
	return &ErrInvalidIPACLName{
		error: fmt.Errorf(`invalid ip_acl_name "%s" provided in backend options "%s"`,
			aclName, backendName),
	}
}

// ErrIPACLSourcePeer is an error type for a peer-source list attached outside a listener.
type ErrIPACLSourcePeer struct {
	error
}

// NewErrIPACLSourcePeer returns an error for a peer-source list on a backend or path.
func NewErrIPACLSourcePeer(aclName, where string) error {
	return &ErrIPACLSourcePeer{
		error: fmt.Errorf("ip acl %q with source peer is listener scope only and cannot be used by %s",
			aclName, where),
	}
}

// NewErrInvalidAuthenticatorName returns a new invalid authenticator name error
func NewErrInvalidAuthenticatorName(authenticatorName, backendName string) error {
	return &ErrInvalidAuthenticatorName{
		error: fmt.Errorf(`invalid authenticator_name "%s" provided in backend options "%s"`,
			authenticatorName, backendName),
	}
}

// ErrInvalidGeoACLName is an error type for a geo_acl_name that names no geo ACL
type ErrInvalidGeoACLName struct {
	error
}

// NewErrInvalidGeoACLName returns a new invalid geo ACL name error
func NewErrInvalidGeoACLName(geoACLName, backendName string) error {
	return &ErrInvalidGeoACLName{
		error: fmt.Errorf(`invalid geo_acl_name %q provided in backend options %q`, geoACLName, backendName),
	}
}

// ErrInvalidTracingName is an error type for invalid tracing name
type ErrInvalidTracingName struct {
	error
}

// NewErrInvalidTracingName returns a new invalid tracing name error
func NewErrInvalidTracingName(tracingName, backendName string) error {
	return &ErrInvalidTracingName{
		error: fmt.Errorf(`invalid tracing_name "%s" provided in backend options "%s"`,
			tracingName, backendName),
	}
}

// ErrInvalidBackendName is an error type for invalid backend name
type ErrInvalidBackendName struct {
	error
}

// NewErrInvalidBackendName returns a new invalid backend name error
func NewErrInvalidBackendName(backendName string) error {
	return &ErrInvalidBackendName{
		error: fmt.Errorf(`invalid backend name: %s`, backendName),
	}
}

// ErrInvalidRewriterName is an error type for invalid rewriter name
type ErrInvalidRewriterName struct {
	error
}

// NewErrInvalidRewriterName returns a new missing invalid rewriter name error
func NewErrInvalidRewriterName(rewriterName, backendName string) error {
	return &ErrInvalidRewriterName{
		error: fmt.Errorf(`invalid rewriter name "%s" provided in backend options "%s"`,
			rewriterName, backendName),
	}
}

// ErrTemplateIsDefault is an error type for a template backend marked default
type ErrTemplateIsDefault struct {
	error
}

// NewErrTemplateIsDefault returns a new template-is-default error
func NewErrTemplateIsDefault(backendName string) error {
	return &ErrTemplateIsDefault{
		error: fmt.Errorf(
			`backend "%s" cannot set both is_template and is_default`,
			backendName),
	}
}

// ErrInvalidTemplateProvider is an error type for a template backend using a
// provider that cannot serve as a template
type ErrInvalidTemplateProvider struct {
	error
}

// NewErrInvalidTemplateProvider returns a new invalid template provider error
func NewErrInvalidTemplateProvider(provider, backendName string) error {
	return &ErrInvalidTemplateProvider{
		error: fmt.Errorf(
			`backend "%s" with provider "%s" cannot be a template; templates must be origin-serving backends`,
			backendName, provider),
	}
}

// ErrTemplatePoolMember is an error type for a template backend referenced in
// a static ALB pool
type ErrTemplatePoolMember struct {
	error
}

// NewErrTemplatePoolMember returns a new template pool member error
func NewErrTemplatePoolMember(poolMemberName, backendName string) error {
	return &ErrTemplatePoolMember{
		error: fmt.Errorf(
			`template backend "%s" cannot be a static pool member of alb "%s"`,
			poolMemberName, backendName),
	}
}

// ErrInvalidDiscovererName is an error type for an alb discovery block
// referencing an undefined discoverer
type ErrInvalidDiscovererName struct {
	error
}

// NewErrInvalidDiscovererName returns a new invalid discoverer name error
func NewErrInvalidDiscovererName(discovererName, backendName string) error {
	return &ErrInvalidDiscovererName{
		error: fmt.Errorf(
			`invalid discoverer name "%s" provided in alb "%s"; it must be defined in the top-level discovery section`,
			discovererName, backendName),
	}
}

// ErrInvalidTemplateBackendName is an error type for an alb discovery block
// referencing a template backend that is undefined or not a template
type ErrInvalidTemplateBackendName struct {
	error
}

// NewErrInvalidTemplateBackendName returns a new invalid template backend
// name error
func NewErrInvalidTemplateBackendName(templateBackendName, backendName string) error {
	return &ErrInvalidTemplateBackendName{
		error: fmt.Errorf(
			`invalid template_backend "%s" provided in alb "%s"; it must name a backend with is_template: true`,
			templateBackendName, backendName),
	}
}

// ErrInvalidTemplateTSMProvider is an error type for a discovery template
// whose provider cannot participate in time-series merging
type ErrInvalidTemplateTSMProvider struct {
	error
}

// NewErrInvalidTemplateTSMProvider returns a new invalid TSM template
// provider error
func NewErrInvalidTemplateTSMProvider(provider, templateName, backendName string) error {
	return &ErrInvalidTemplateTSMProvider{
		error: fmt.Errorf(
			`template_backend "%s" with provider "%s" in alb "%s" must use a time-series-merge-capable provider for tsmerge mechanisms or replica_group_label`,
			templateName, provider, backendName),
	}
}

// ErrInvalidHost is an error type for an invalid entry in a backend's hosts list
type ErrInvalidHost struct {
	Host        string
	BackendName string
	Reason      string
}

// NewErrInvalidHost returns a new invalid host error
func NewErrInvalidHost(host, backendName, reason string) error {
	return &ErrInvalidHost{Host: host, BackendName: backendName, Reason: reason}
}

func (e *ErrInvalidHost) Error() string {
	return fmt.Sprintf("invalid host %q for backend %s: %s", e.Host, e.BackendName, e.Reason)
}

// ErrAnyHostRoutingWithHosts indicates a backend sets both any_host_routing
// and a hosts list; any_host_routing already serves every hostname
var ErrAnyHostRoutingWithHosts = errors.New(
	"any_host_routing cannot be combined with hosts")
