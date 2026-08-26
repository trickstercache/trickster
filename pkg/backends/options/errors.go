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

// NewErrInvalidAuthenticatorName returns a new invalid authenticator name error
func NewErrInvalidAuthenticatorName(authenticatorName, backendName string) error {
	return &ErrInvalidAuthenticatorName{
		error: fmt.Errorf(`invalid authenticator_name "%s" provided in backend options "%s"`,
			authenticatorName, backendName),
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
