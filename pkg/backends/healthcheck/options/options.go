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
	"net/url"
	"strings"
	"time"

	"github.com/tricksterproxy/trickster/cmd/trickster/config/defaults"
	d "github.com/tricksterproxy/trickster/cmd/trickster/config/defaults"
	"github.com/tricksterproxy/trickster/pkg/proxy/headers"

	"github.com/BurntSushi/toml"
)

// MaxProbeWaitMS is the maximum time a health check will wait before timing out
const MaxProbeWaitMS = 30000

// MinProbeWaitMS is the minimum time a health check will wait before timing out
const MinProbeWaitMS = 100

// ErrNoOptionsProvided returns an error for no health check options provided
var ErrNoOptionsProvided = errors.New("no health check options provided")

// Options defines Health Checking Options
type Options struct {

	// IntervalMS defines the interval in milliseconds at which the target will be probed
	IntervalMS int `toml:"interval_ms"`
	// FailureThreshold indicates the number of consecutive failed probes required to
	// mark an available target as unavailable
	FailureThreshold int `toml:"failure_threshold"`
	// RecoveryThreshold indicates the number of consecutive successful probes required to
	// mark an unavailable target as available
	RecoveryThreshold int `toml:"recovery_threshold"`

	// Target Outbound Request Options
	// Verb provides the HTTP verb to use when making an upstream health check
	Verb string `toml:"verb"`
	// Scheme is the scheme to use when making an upstream health check (http or https)
	Scheme string `toml:"scheme"`
	// Host is the Host name header to use when making an upstream health check
	Host string `toml:"host"`
	// Path provides the URL path for the upstream health check
	Path string `toml:"path"`
	// Query provides the HTTP query parameters to use when making an upstream health check
	Query string `toml:"query"`
	// Headers provides the HTTP Headers to apply when making an upstream health check
	Headers map[string]string `toml:"headers"`
	// Body provides a body to apply when making an upstream health check request
	Body string `toml:"body"`
	// TimeoutMS is the amount of time a health check probe should wait for a response
	// before timing out
	TimeoutMS int `toml:"timeout_ms"`
	// Target Probe Response Options
	// ExpectedCodes is the list of Status Codes that positively indicate a Healthy status
	ExpectedCodes []int `toml:"expected_codes"`
	// ExpectedHeaders is a list of Headers (name and value) expected in the response
	// in order to be considered Healthy status
	ExpectedHeaders map[string]string `toml:"expected_headers"`
	// ExpectedBody is the body expected in the response to be considered Healthy status
	ExpectedBody string `toml:"expected_body"`

	md              *toml.MetaData
	hasExpectedBody bool
}

// New returns a new Options reference with default values
func New() *Options {
	return &Options{
		Verb:              d.DefaultHealthCheckVerb,
		Scheme:            "http",
		Headers:           make(map[string]string),
		Path:              d.DefaultHealthCheckPath,
		Query:             d.DefaultHealthCheckQuery,
		ExpectedCodes:     []int{200},
		FailureThreshold:  d.DefaultHealthCheckFailureThreshold,
		RecoveryThreshold: d.DefaultHealthCheckRecoveryThreshold,
	}
}

// SetMetaData sets the TOML metadata for the health checker options
func (o *Options) SetMetaData(md *toml.MetaData) {
	o.md = md
}

// Clone returns an exact copy of a *healthcheck.Options
func (o *Options) Clone() *Options {
	c := &Options{}
	c.Verb = o.Verb
	c.Scheme = o.Scheme
	c.Host = o.Host
	c.Path = o.Path
	c.Query = o.Query
	c.Body = o.Body
	c.IntervalMS = o.IntervalMS
	c.ExpectedBody = o.ExpectedBody
	if o.Headers != nil {
		c.Headers = headers.Lookup(o.Headers).Clone()
	}
	if o.ExpectedHeaders != nil {
		c.ExpectedHeaders = headers.Lookup(o.ExpectedHeaders).Clone()
	}
	if len(o.ExpectedCodes) > 0 {
		c.ExpectedCodes = make([]int, len(o.ExpectedCodes))
		for i, v := range o.ExpectedCodes {
			c.ExpectedCodes[i] = v
		}
	}
	c.md = o.md
	c.hasExpectedBody = o.hasExpectedBody
	return c
}

func (o *Options) Overlay(name string, custom *Options) {
	if custom == nil || custom.md == nil {
		return
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "upstream_path") {
		o.Path = custom.Path
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "verb") {
		o.Verb = custom.Verb
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "query") {
		o.Query = custom.Query
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "headers") {
		o.Headers = custom.Headers
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "body") {
		o.Body = custom.Body
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "expected_codes") {
		o.ExpectedCodes = custom.ExpectedCodes
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "expected_body") {
		o.ExpectedBody = custom.ExpectedBody
		o.hasExpectedBody = true
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "expected_headers") {
		o.ExpectedHeaders = custom.ExpectedHeaders
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "interval_ms") {
		o.IntervalMS = custom.IntervalMS
	}
}

// URL returns a URL from the Options
func (o *Options) URL() *url.URL {
	u := &url.URL{}
	u.Scheme = o.Scheme
	u.Host = o.Host
	u.Path = o.Path
	if strings.HasPrefix(o.Query, "?") {
		o.Query = o.Query[1:]
	}
	u.RawQuery = o.Query
	return u
}

// HasExpectedBody returns true if a Custom Expected Body was provided
func (o *Options) HasExpectedBody() bool {
	return o.hasExpectedBody
}

// SetExpectedBody sets the expected body
func (o *Options) SetExpectedBody(body string) {
	o.hasExpectedBody = true
	o.ExpectedBody = body
}

// CalibrateTimeout returns a time.Duration representing a calibrated
// timeout value based on the milliseconds of duration provided
func CalibrateTimeout(ms int) time.Duration {
	if ms > MaxProbeWaitMS {
		ms = MaxProbeWaitMS
	} else if ms <= 0 {
		ms = defaults.DefaultHealthCheckTimeoutMS
	} else if ms < MinProbeWaitMS {
		ms = MinProbeWaitMS
	}
	return time.Duration(ms) * time.Millisecond
}
