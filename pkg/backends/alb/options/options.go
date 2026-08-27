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
	"slices"
	"strings"

	te "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	mechoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/options"
	tsmoptions "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm/options"
	ur "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"go.yaml.in/yaml/v3"
)

// Options defines options for ALBs
type Options struct {
	// MechanismName indicates the name of the load balancing mechanism
	MechanismName string `yaml:"mechanism,omitempty"`
	// Pool provides the list of backend names to be used by the load balancer
	Pool []string `yaml:"pool,omitempty"`
	// HealthyFloor is the minimum health check status value admitted to the pool.
	// Values below 0 admit members the probe has confirmed Failing; only set
	// this below 0 if you intend to route to known-broken upstreams.
	// -1 : all pool members admitted, including Failing
	//  0 (default) : Unknown (0) and Healthy (1) admitted, Failing rejected
	//  1 : only Healthy (1) admitted
	// Unknown means the first health check hasn't returned yet, or the target
	// backend has no health check interval configured.
	HealthyFloor int `yaml:"healthy_floor,omitempty"`
	// MaxCaptureBytes overrides the backend-level max_capture_bytes for this
	// ALB's fanout members. Set this when the ALB's expected response shape
	// differs from the backend default (e.g. a TSM fan-out of 50 small-payload
	// shards may safely use a lower cap than the global default to surface
	// runaway upstreams faster). When 0, falls back to the parent Backend's
	// max_capture_bytes, then to the package-level default (256 MiB).
	MaxCaptureBytes int `yaml:"max_capture_bytes,omitempty"`
	// MaxFanoutCaptureBytes, if > 0, caps the aggregate in-flight
	// capture-buffer reservations across all slots in one ALB fanout call.
	// When 0, falls back to the parent Backend's max_fanout_capture_bytes,
	// which itself defaults to 0 (no aggregate cap).
	MaxFanoutCaptureBytes int `yaml:"max_fanout_capture_bytes,omitempty"`
	// OutputFormat selects the provider used by the time-series merge mechanism.
	OutputFormat string `yaml:"output_format,omitempty"`
	// Deprecated: use fgr.status_codes instead of this top-level option
	// FGRStatusCodes provides an explicit list of status codes considered "good" when using
	// the First Good Response (fgr) methodology. By default, any code < 400 is good.
	FGRStatusCodes []int `yaml:"fgr_status_codes,omitempty"`
	// UserRouter provides options for the User Router mechanism
	UserRouter *ur.Options `yaml:"user_router,omitempty"`
	//
	// synthetic values
	FgrCodesLookup sets.Set[int] `yaml:"-"`

	// mechanism-specific options
	TSMOptions tsmoptions.Options        `yaml:"tsm,omitempty"`
	NLMOptions NewestLastModifiedOptions `yaml:"nlm,omitempty"`
	FGROptions FirstGoodResponseOptions  `yaml:"fgr,omitempty"`
}

type FirstGoodResponseOptions struct {
	// StatusCodes provides an explicit list of status codes considered "good" when using
	// the First Good Response (fgr) methodology. By default, any code < 400 is good.
	StatusCodes        []int              `yaml:"status_codes,omitempty"`
	ConcurrencyOptions ConcurrencyOptions `yaml:",inline"`
}

// TimeSeriesMergeOptions preserves the ALB name for TSM-owned options.
type TimeSeriesMergeOptions = tsmoptions.Options

type NewestLastModifiedOptions struct {
	ConcurrencyOptions ConcurrencyOptions `yaml:",inline"`
}

// ConcurrencyOptions preserves the ALB name for shared mechanism options.
type ConcurrencyOptions = mechoptions.ConcurrencyOptions

// InvalidALBOptionsError is an error type for invalid ALB Options
type InvalidALBOptionsError struct {
	error
}

var _ types.ConfigOptions[Options] = &Options{}

const defaultTSOutputFormat = providers.Prometheus

var (
	ErrUserRouterRequired     = errors.New("'user_router' block is required")
	ErrInvalidOutputFormat    = errors.New("value for 'output_format' is invalid")
	ErrOutputFormatOnlyForTSM = errors.New("'output_format' option is only valid for provider 'alb' and mechanism 'tsmerge'")
)

// NewErrInvalidALBOptions returns an invalid ALB Options error
func NewErrInvalidALBOptions(backendName string) error {
	return &InvalidALBOptionsError{
		error: fmt.Errorf("invalid alb options for backend [%s]",
			backendName),
	}
}

// New returns a New Options object with the default values
func New() *Options {
	return &Options{}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {
	var fsc []int
	var fscm sets.Set[int]

	if o.FGRStatusCodes != nil {
		fsc = make([]int, len(o.FGRStatusCodes))
		copy(fsc, o.FGRStatusCodes)
	}

	if o.FgrCodesLookup != nil {
		fscm = o.FgrCodesLookup.Clone()
	}

	c := pointers.Clone(o)
	if o.UserRouter != nil {
		c.UserRouter = o.UserRouter.Clone()
	}
	c.Pool = slices.Clone(o.Pool)
	c.FGRStatusCodes = fsc
	c.FgrCodesLookup = fscm
	return c
}

func (o *Options) Initialize(_ string) error {
	if strings.HasPrefix(o.MechanismName, names.MechanismTSM) && o.MechanismName != names.MechanismTSM {
		// shorten from tsmerge to tsm
		o.MechanismName = names.MechanismTSM
	}
	switch o.MechanismName {
	case names.MechanismFGR:
		// apply deprecated top-level FGRStatusCodes to new FROptions level
		if len(o.FGRStatusCodes) > 0 && len(o.FGROptions.StatusCodes) == 0 {
			o.FGROptions.StatusCodes = o.FGRStatusCodes
		}
		if len(o.FGROptions.StatusCodes) > 0 {
			o.FgrCodesLookup = sets.NewIntSet()
			o.FgrCodesLookup.SetAll(o.FGROptions.StatusCodes)
		}
	case names.MechanismTSM:
		if o.OutputFormat == "" {
			o.OutputFormat = defaultTSOutputFormat
		}
	}

	return nil
}

func (o *Options) Validate() (bool, error) {
	switch o.MechanismName {
	case names.MechanismUR:
		if o.UserRouter == nil {
			return false, ErrUserRouterRequired
		}
	case names.MechanismTSM:
		if o.OutputFormat != "" && !providers.IsSupportedTimeSeriesMergeProvider(o.OutputFormat) {
			return false, ErrInvalidOutputFormat
		}
	default:
		if o.OutputFormat != "" {
			return false, ErrOutputFormatOnlyForTSM
		}
	}
	return true, nil
}

func (o *Options) ValidatePool(backendName string, allBackends sets.Set[string]) error {
	for _, bn := range o.Pool {
		if _, ok := allBackends[bn]; !ok {
			return te.NewErrInvalidPoolMemberName(backendName, bn)
		}
	}
	return nil
}

// ValidateNoCycles walks the ALB reference graph and returns an error if any
// ALB transitively references itself. The input maps ALB-backend name to its
// Options; non-ALB targets are leaves and ignored. A back edge to a node
// currently on the DFS stack is reported as a cycle.
//
// Edges considered: (a) every entry of o.Pool, and (b) for ALBs configured
// with the user_router mechanism, o.UserRouter.DefaultBackend plus every
// o.UserRouter.Users[*].ToBackend. Without the user_router edges, a config
// like alb1.mechanism=user_router with user_router.default_backend=alb1
// passes validation and exhausts the goroutine stack on the first request.
func ValidateNoCycles(albs map[string]*Options) error {
	const (
		unseen   = 0
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(albs))
	var visit func(name string, path []string) error
	visit = func(name string, path []string) error {
		switch state[name] {
		case visiting:
			// back edge: cycle detected; render the cycle for the operator.
			// slices.Index can return -1 if state is corrupt; clamp to 0.
			start := max(slices.Index(path, name), 0)
			cyc := append(slices.Clone(path[start:]), name)
			return fmt.Errorf("cycle detected in alb pool references: %s "+
				"(previously caused stack overflow at request time; "+
				"now rejected at startup)",
				strings.Join(cyc, " -> "))
		case done:
			return nil
		}
		o, ok := albs[name]
		if !ok {
			// not an ALB; leaf for cycle purposes
			return nil
		}
		state[name] = visiting
		path = append(path, name)
		for _, target := range albEdges(o) {
			if _, isALB := albs[target]; !isALB {
				continue
			}
			if err := visit(target, path); err != nil {
				return err
			}
		}
		state[name] = done
		return nil
	}
	for name := range albs {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// albEdges returns every backend name this ALB can dispatch to. For pool-
// based mechanisms this is just o.Pool; for user_router-typed ALBs it also
// includes UserRouter.DefaultBackend and every Users[*].ToBackend.
func albEdges(o *Options) []string {
	edges := make([]string, 0, len(o.Pool))
	edges = append(edges, o.Pool...)
	if o.UserRouter != nil {
		if o.UserRouter.DefaultBackend != "" {
			edges = append(edges, o.UserRouter.DefaultBackend)
		}
		for _, u := range o.UserRouter.Users {
			if u != nil && u.ToBackend != "" {
				edges = append(edges, u.ToBackend)
			}
		}
	}
	return edges
}

func (o *Options) UnmarshalYAML(value *yaml.Node) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := value.Decode(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
