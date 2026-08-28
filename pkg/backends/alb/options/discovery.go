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

	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Startup policies for a discovery-backed ALB whose discoverer is
// unavailable at startup
const (
	// StartupPolicyRetry starts the ALB with only its static pool members
	// (possibly none) and keeps retrying discovery in the background
	StartupPolicyRetry = "retry"
	// StartupPolicyFail fails startup when the discoverer cannot deliver an
	// initial snapshot
	StartupPolicyFail = "fail"
)

// Health modes for discovered members
const (
	// HealthModeProbe (default): discovered members inherit the template's
	// active healthcheck; members with no probe interval remain Unchecked
	// (and trigger the healthy_floor reset guardrail when the floor
	// requires Passing)
	HealthModeProbe = "probe"
	// HealthModeProvider: the discoverer's readiness reporting drives the
	// member's health status in place of active probes (e.g., kubernetes
	// EndpointSlice ready/terminating conditions). Provider-ready members
	// enter the pool as Passing; readiness-unknown members as Unchecked, so
	// a healthy_floor of 1 excludes them until the provider reports ready.
	HealthModeProvider = "provider"
)

var (
	ErrDiscovererNameRequired = errors.New(
		"'discoverer_name' is required in the alb discovery block")
	ErrTemplateBackendRequired = errors.New(
		"'template_backend' is required in the alb discovery block")
	ErrDiscoveryQueryRequired = errors.New(
		"a 'query' is required in the alb discovery block")
	ErrInvalidStartupPolicy = errors.New(
		"'startup_policy' must be retry or fail")
	ErrInvalidMinMembers = errors.New(
		"'min_members' cannot be negative")
	ErrInvalidDebounceWindow = errors.New(
		"'debounce_window' cannot be negative")
	ErrInvalidHealthMode = errors.New(
		"'health_mode' must be probe or provider")
)

// DiscoveryOptions binds an ALB's pool to a named discoverer from the
// top-level 'discovery' config section. Discovered members are additive to
// any static Pool entries.
type DiscoveryOptions struct {
	// DiscovererName references a named discoverer in the top-level
	// 'discovery' config section
	DiscovererName string `yaml:"discoverer_name,omitempty"`
	// TemplateBackend names a backend configured with is_template: true,
	// which is cloned for each discovered member
	TemplateBackend string `yaml:"template_backend,omitempty"`
	// Query defines what to select from the discoverer; its usable fields
	// depend on the discoverer's provider
	Query *do.Query `yaml:"query,omitempty"`
	// MinMembers is the guardrail floor for accepted snapshots: a snapshot
	// that would shrink the discovered membership below this count is
	// rejected, the last-good membership is kept, and the rejection is
	// surfaced via metrics/logs. 0 disables the floor.
	MinMembers int `yaml:"min_members,omitempty"`
	// DebounceWindow damps flapping sources by coalescing snapshots arriving
	// within the window into one pool update. 0 disables damping.
	DebounceWindow timeconv.Duration `yaml:"debounce_window,omitempty"`
	// StartupPolicy controls behavior when the discoverer cannot deliver an
	// initial snapshot at startup: retry (default) or fail
	StartupPolicy string `yaml:"startup_policy,omitempty"`
	// HealthMode controls how discovered members' health is determined:
	// probe (default) or provider
	HealthMode string `yaml:"health_mode,omitempty"`
}

// Clone returns a perfect copy of the DiscoveryOptions
func (d *DiscoveryOptions) Clone() *DiscoveryOptions {
	out := pointers.Clone(d)
	if d.Query != nil {
		out.Query = d.Query.Clone()
	}
	return out
}

// Initialize applies default values to the DiscoveryOptions
func (d *DiscoveryOptions) Initialize(_ string) error {
	if d.StartupPolicy == "" {
		d.StartupPolicy = StartupPolicyRetry
	}
	if d.HealthMode == "" {
		d.HealthMode = HealthModeProbe
	}
	return nil
}

// Validate validates the DiscoveryOptions in isolation. Cross-references
// (discoverer name resolution, provider-specific query validation, template
// backend checks) are validated at the config level, where the discoverer
// and backend lookups are in scope.
func (d *DiscoveryOptions) Validate() (bool, error) {
	if d.DiscovererName == "" {
		return false, ErrDiscovererNameRequired
	}
	if d.TemplateBackend == "" {
		return false, ErrTemplateBackendRequired
	}
	if d.Query == nil {
		return false, ErrDiscoveryQueryRequired
	}
	if d.MinMembers < 0 {
		return false, ErrInvalidMinMembers
	}
	if d.DebounceWindow < 0 {
		return false, ErrInvalidDebounceWindow
	}
	switch d.StartupPolicy {
	case "", StartupPolicyRetry, StartupPolicyFail:
	default:
		return false, fmt.Errorf("%w (got %q)", ErrInvalidStartupPolicy,
			d.StartupPolicy)
	}
	switch d.HealthMode {
	case "", HealthModeProbe, HealthModeProvider:
	default:
		return false, fmt.Errorf("%w (got %q)", ErrInvalidHealthMode,
			d.HealthMode)
	}
	return true, nil
}
