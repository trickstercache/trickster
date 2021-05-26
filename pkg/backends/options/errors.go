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

// ErrInvalidMaxShardSizeMS is an error for when 'shard_max_size_ms' is not
// a multiple 'shard_step_ms'
var ErrInvalidMaxShardSizeMS = errors.New(
	"'shard_max_size_ms' must be a multiple of 'shard_step_ms' when both are non-zero")

// ErrInvalidMaxShardSize is an error for when both 'shard_max_size_ms' and
// 'shard_max_size_points' are used on the same backend
var ErrInvalidMaxShardSize = errors.New(
	"'shard_max_size_ms' and 'shard_max_size_points' cannot both be non-zero")

// ErrMissingProvider is an error type for missing provider
type ErrMissingProvider struct {
	error
}

// NewErrMissingProvider returns a new missing provider error
func NewErrMissingProvider(backendName string) error {
	var e *ErrMissingProvider = &ErrMissingProvider{
		error: fmt.Errorf(`missing provider for backend "%s"`, backendName),
	}
	return e
}

// ErrMissingOriginURL is an error type for missing origin URL
type ErrMissingOriginURL struct {
	error
}

// NewErrMissingOriginURL returns a new missing origin URL error
func NewErrMissingOriginURL(backendName string) error {
	var e *ErrMissingOriginURL = &ErrMissingOriginURL{
		error: fmt.Errorf(`missing origin-url for backend "%s"`, backendName),
	}
	return e
}

// ErrInvalidNegativeCacheName is an error type for invalid negative cache name
type ErrInvalidNegativeCacheName struct {
	error
}

// NewErrInvalidNegativeCacheName returns a new invalid negative cache name error
func NewErrInvalidNegativeCacheName(cacheName string) error {
	var e *ErrInvalidNegativeCacheName = &ErrInvalidNegativeCacheName{
		error: fmt.Errorf(`invalid negative cache name: %s`, cacheName),
	}
	return e
}

// ErrInvalidRuleName is an error type for invalid rule name
type ErrInvalidRuleName struct {
	error
}

// NewErrInvalidRuleName returns a new invalid rule name error
func NewErrInvalidRuleName(ruleName, backendName string) error {
	var e *ErrInvalidRuleName = &ErrInvalidRuleName{
		error: fmt.Errorf(`invalid rule name "%s" provided in backend options "%s"`,
			ruleName, backendName),
	}
	return e
}

// ErrInvalidALBOptions is an error type for invalid ALB Options
type ErrInvalidALBOptions struct {
	error
}

// NewErrInvalidALBOptions returns a new invalid ALB Options error
func NewErrInvalidALBOptions(albName, backendName string) error {
	var e *ErrInvalidALBOptions = &ErrInvalidALBOptions{
		error: fmt.Errorf("invalid backend name [%s] provided in pool for alb [%s]",
			backendName, albName),
	}
	return e
}

// ErrInvalidCacheName is an error type for invalid cache name
type ErrInvalidCacheName struct {
	error
}

// NewErrInvalidCacheName returns a new invalid cache name error
func NewErrInvalidCacheName(cacheName, backendName string) error {
	var e *ErrInvalidCacheName = &ErrInvalidCacheName{
		error: fmt.Errorf(`invalid cache name "%s" provided in backend options "%s"`,
			cacheName, backendName),
	}
	return e
}

// ErrInvalidBackendName is an error type for invalid backend name
type ErrInvalidBackendName struct {
	error
}

// NewErrInvalidBackendName returns a new invalid backend name error
func NewErrInvalidBackendName(backendName string) error {
	var e *ErrInvalidBackendName = &ErrInvalidBackendName{
		error: fmt.Errorf(`invalid backend name: %s`, backendName),
	}
	return e
}

// ErrInvalidRewriterName is an error type for invalid rewriter name
type ErrInvalidRewriterName struct {
	error
}

// NewErrInvalidRewriterName returns a new missing invalid rewriter name error
func NewErrInvalidRewriterName(rewriterName, backendName string) error {
	var e *ErrInvalidRewriterName = &ErrInvalidRewriterName{
		error: fmt.Errorf(`invalid rewriter name "%s" provided in backend options "%s"`,
			rewriterName, backendName),
	}
	return e
}
