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
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
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

const defaultTSOutputFormat = providers.Prometheus

// InvalidALBOptionsError is an error type for invalid ALB Options
type InvalidALBOptionsError struct {
	error
}

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

	c := &Options{
		MechanismName:  o.MechanismName,
		HealthyFloor:   o.HealthyFloor,
		OutputFormat:   o.OutputFormat,
		FgrCodesLookup: fscm,
		FGRStatusCodes: fsc,
	}
	if o.UserRouter != nil {
		c.UserRouter = o.UserRouter.Clone()
	}
	c.Pool = slices.Clone(o.Pool)
	return c
}

func (o *Options) Initialize() error {
	if strings.HasPrefix(o.MechanismName, "tsm") && o.MechanismName != "tsm" {
		// shorten from tsmerge to tsm
		o.MechanismName = "tsm"
	}
	switch o.MechanismName {
	case "fgr":
		if len(o.FGRStatusCodes) > 0 {
			o.FgrCodesLookup = sets.NewIntSet()
			o.FgrCodesLookup.SetAll(o.FGRStatusCodes)
		}
	case "tsm":
		if o.OutputFormat == "" {
			o.OutputFormat = defaultTSOutputFormat
		}
	}

	return nil
}

func (o *Options) Validate() error {
	switch o.MechanismName {
	case "ur":
		if o.UserRouter == nil {
			return ErrUserRouterRequired
		}
	case "tsm":
		if o.OutputFormat != "" && !providers.IsSupportedTimeSeriesMergeProvider(o.OutputFormat) {
			return ErrInvalidOutputFormat
		}
	default:
		if o.OutputFormat != "" {
			return ErrOutputFormatOnlyForTSM
		}
	}
	return nil
}

func (o *Options) ValidatePool(backendName string, allBackends sets.Set[string]) error {
	for _, bn := range o.Pool {
		if _, ok := allBackends[bn]; !ok {
			return te.NewErrInvalidPoolMemberName(backendName, bn)
		}
	}
	return nil
}

type loaderOptions struct {
	MechanismName  *string     `yaml:"mechanism,omitempty"`
	Pool           []string    `yaml:"pool,omitempty"`
	HealthyFloor   *int        `yaml:"healthy_floor,omitempty"`
	OutputFormat   *string     `yaml:"output_format,omitempty"`
	FGRStatusCodes []int       `yaml:"fgr_status_codes,omitempty"`
	UserRouter     *ur.Options `yaml:"user_router,omitempty"`
}

func (o *Options) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*o = *(New())

	var load loaderOptions
	if err := unmarshal(&load); err != nil {
		return err
	}

	if load.MechanismName != nil {
		o.MechanismName = *load.MechanismName
	}
	if load.Pool != nil {
		o.Pool = load.Pool
	}
	if load.HealthyFloor != nil {
		o.HealthyFloor = *load.HealthyFloor
	}
	if load.OutputFormat != nil {
		o.OutputFormat = *load.OutputFormat
	}
	if load.FGRStatusCodes != nil {
		o.FGRStatusCodes = load.FGRStatusCodes
	}
	if load.UserRouter != nil {
		o.UserRouter = load.UserRouter
	}

	return nil
}
