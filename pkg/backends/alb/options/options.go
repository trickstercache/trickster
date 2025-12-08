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
	ur "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// Options defines options for ALBs
type Options struct {
	// MechanismName indicates the name of the load balancing mechanism
	MechanismName string `yaml:"mechanism,omitempty"`
	// Pool provides the list of backend names to be used by the load balancer
	Pool []string `yaml:"pool,omitempty"`
	// HealthyFloor is the minimum health check status value to be considered Available in the pool
	// -1 : all pool members are Available regardless of health check status
	//  0 (default) : pool members with status of unknown (0) or healthy (1) are Available
	//  1 : only pool members with status of healthy (1) are Available
	// unknown means the first hc hasn't returned yet,
	// or (more likely) HealthCheck Interval on target backend is not set
	HealthyFloor int `yaml:"healthy_floor,omitempty"`
	// OutputFormat accompanies the tsmerge Mechanism to indicate the provider output format
	// options include any valid time seres backend like prometheus, influxdb or clickhouse
	OutputFormat string `yaml:"output_format,omitempty"`
	// FGRStatusCodes provides an explicit list of status codes considered "good" when using
	// the First Good Response (fgr) methodology. By default, any code < 400 is good.
	FGRStatusCodes []int `yaml:"fgr_status_codes,omitempty"`
	// UserRouter provides options for the User Router mechanism
	UserRouter *ur.Options `yaml:"user_router,omitempty"`
	//
	// synthetic values
	FgrCodesLookup sets.Set[int] `yaml:"-"`
}

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
		if len(o.FGRStatusCodes) > 0 {
			o.FgrCodesLookup = sets.NewIntSet()
			o.FgrCodesLookup.SetAll(o.FGRStatusCodes)
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

func (o *Options) UnmarshalYAML(unmarshal func(any) error) error {
	type loadOptions Options
	lo := loadOptions(*(New()))
	if err := unmarshal(&lo); err != nil {
		return err
	}
	*o = Options(lo)
	return nil
}
