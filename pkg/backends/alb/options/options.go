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
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/util/copiers"
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
	FGRStatusCodes []int `yaml:"fgr_status_codes"`
	//
	// synthetic values
	FgrCodesLookup map[int]interface{} `yaml:"-"`
}

const defaultOutputFormat = "prometheus"

// New returns a New Options object with the default values
func New() *Options {
	return &Options{}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {

	var fsc []int
	var fscm map[int]interface{}

	if o.FGRStatusCodes != nil {
		fsc = make([]int, len(o.FGRStatusCodes))
		copy(fsc, o.FGRStatusCodes)
	}

	if o.FgrCodesLookup != nil {
		fscm = make(map[int]interface{})
		for k, v := range o.FgrCodesLookup {
			fscm[k] = v
		}
	}

	c := &Options{
		MechanismName:  o.MechanismName,
		HealthyFloor:   o.HealthyFloor,
		OutputFormat:   o.OutputFormat,
		FgrCodesLookup: fscm,
		FGRStatusCodes: fsc,
	}
	c.Pool = copiers.CopyStrings(o.Pool)
	return c
}

// SetDefaults iterates the provided Options, and overlays user-set values onto the default Options
func SetDefaults(name string, options *Options, metadata yamlx.KeyLookup) (*Options, error) {

	if metadata == nil {
		return nil, nil // todo: add error
	}

	o := New()

	if !metadata.IsDefined("backends", name, "alb") {
		return nil, nil
	}

	if metadata.IsDefined("backends", name, "alb", "pool") {
		o.Pool = options.Pool
	}

	if metadata.IsDefined("backends", name, "alb", "mechanism") && options.MechanismName != "" {
		o.MechanismName = options.MechanismName
	}

	if metadata.IsDefined("backends", name, "alb", "healthy_floor") && options.HealthyFloor > 0 {
		o.HealthyFloor = options.HealthyFloor
	}

	if o.MechanismName == "fgr" {
		if metadata.IsDefined("backends", name, "alb", "fgr_status_codes") && options.FGRStatusCodes != nil {
			o.FGRStatusCodes = options.FGRStatusCodes
		}
		if o.FGRStatusCodes != nil {
			o.FgrCodesLookup = make(map[int]interface{})
			for _, i := range o.FGRStatusCodes {
				o.FgrCodesLookup[i] = nil
			}
		}
	}

	if metadata.IsDefined("backends", name, "alb", "output_format") && options.OutputFormat != "" {
		if !strings.HasPrefix(o.MechanismName, "tsm") {
			return nil, errors.New("'output_format' option is only valid for provider 'alb' and mechanism 'tsmerge'")
		}
		o.OutputFormat = options.OutputFormat
		if !providers.IsSupportedTimeSeriesMergeProvider(o.OutputFormat) {
			return nil, errors.New("value for 'output_format' is invalid")
		}
	}

	if strings.HasPrefix(o.MechanismName, "tsm") && o.OutputFormat == "" {
		o.OutputFormat = defaultOutputFormat
	}

	return o, nil

}
