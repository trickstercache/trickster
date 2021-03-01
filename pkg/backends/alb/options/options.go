/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/tricksterproxy/trickster/pkg/backends/providers"

	"github.com/BurntSushi/toml"
)

// Options defines options for ALBs
type Options struct {
	// MechanismName indicates the name of the load balancing mechanism
	MechanismName string `toml:"mechanism"`
	// Pool provides the list of backend names to be used by the load balancer
	Pool []string `toml:"pool"`
	// HealthyFloor is the minimum health check status value to be considered Available in the pool
	// -1 : all pool members are Available regardless of health check status
	//  0 (default) : pool members with status of unknown (0) or healthy (1) are Available
	//  1 : only pool members with status of healthy (1) are Available
	// unknown means the first hc hasn't returned yet, or (more likely) HealthCheck Interval on target backend is not set
	HealthyFloor int `toml:"healthy_floor"`
	// OutputFormat accompanies the tsmerge Mechanism to indicate the provider output format
	// options include any valid time seres backend like prometheus, influxdb or clickhouse
	OutputFormat string `toml:"output_format"`
	// MergeablePaths are ones that Trickster can merge multiple documents into a single response
	MergeablePaths []string `toml:"-"` // this is populated by backends that support tsmerge

}

// New returns a New Options object with the default values
func New() *Options {
	return &Options{}
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options {

	c := &Options{
		MechanismName: o.MechanismName,
		HealthyFloor:  o.HealthyFloor,
		OutputFormat:  o.OutputFormat,
	}

	if o.Pool != nil {
		c.Pool = make([]string, len(o.Pool))
		if len(o.Pool) > 0 {
			copy(c.Pool, o.Pool)
		}
	}

	if o.MergeablePaths != nil {
		c.MergeablePaths = make([]string, len(o.MergeablePaths))
		if len(o.MergeablePaths) > 0 {
			copy(c.MergeablePaths, o.MergeablePaths)
		}
	}

	return c
}

// ProcessTOML iterates a TOML Config and incorporates into the base Options
func ProcessTOML(name string, options *Options, metadata *toml.MetaData) (*Options, error) {

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

	if metadata.IsDefined("backends", name, "alb", "output_format") && options.OutputFormat != "" {
		if !strings.HasPrefix(o.MechanismName, "tsm") {
			return nil, errors.New("'output_format' option is only valid for provider 'alb' and mechanism 'tsmerge'")
		}
		o.OutputFormat = options.OutputFormat
		if !providers.IsSupportedTimeSeriesProvider(o.OutputFormat) {
			return nil, errors.New("value for 'output_format' is invalid")
		}
	}

	return o, nil

}
