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
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
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

// OverlayYAMLData extracts supported ALB Options values from the yaml map,
// and returns a new default Options overlaid with the extracted values
func OverlayYAMLData(name string, options *Options,
	y yamlx.KeyLookup) (*Options, error) {

	if y == nil {
		return nil, te.ErrInvalidOptionsMetadata
	}

	o := New()

	if !y.IsDefined("backends", name, providers.ALB) {
		return nil, nil
	}

	if y.IsDefined("backends", name, providers.ALB, "pool") {
		o.Pool = options.Pool
	}

	if y.IsDefined("backends", name, providers.ALB, "mechanism") && options.MechanismName != "" {
		o.MechanismName = options.MechanismName
	}

	if y.IsDefined("backends", name, providers.ALB, "healthy_floor") && options.HealthyFloor > 0 {
		o.HealthyFloor = options.HealthyFloor
	}

	if o.MechanismName == "fgr" {
		if y.IsDefined("backends", name, providers.ALB, "fgr_status_codes") && options.FGRStatusCodes != nil {
			o.FGRStatusCodes = options.FGRStatusCodes
		}
		if o.FGRStatusCodes != nil {
			o.FgrCodesLookup = sets.NewIntSet()
			o.FgrCodesLookup.SetAll(o.FGRStatusCodes)
		}
	}

	if o.MechanismName == "ur" && options.UserRouter != nil {
		var err error
		o.UserRouter, err = ur.OverlayYAMLData(name, options.UserRouter, y)
		if err != nil {
			return nil, err
		}
	}

	if y.IsDefined("backends", name, providers.ALB, "output_format") && options.OutputFormat != "" {
		if !strings.HasPrefix(o.MechanismName, "tsm") {
			return nil, errors.New("'output_format' option is only valid for provider 'alb' and mechanism 'tsmerge'")
		}
		o.OutputFormat = options.OutputFormat
		if !providers.IsSupportedTimeSeriesMergeProvider(o.OutputFormat) {
			return nil, errors.New("value for 'output_format' is invalid")
		}
	}

	if strings.HasPrefix(o.MechanismName, "tsm") && o.OutputFormat == "" {
		o.OutputFormat = defaultTSOutputFormat
	}
	return o, nil
}

func (o *Options) ValidatePool(backendName string, allBackends sets.Set[string]) error {
	for _, bn := range o.Pool {
		if _, ok := allBackends[bn]; !ok {
			return te.NewErrInvalidPoolMemberName(backendName, bn)
		}
	}
	return nil
}
