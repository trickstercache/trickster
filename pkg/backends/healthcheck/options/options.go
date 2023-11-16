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
	"net/url"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
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
	IntervalMS int `yaml:"interval_ms,omitempty"`
	// FailureThreshold indicates the number of consecutive failed probes required to
	// mark an available target as unavailable
	FailureThreshold int `yaml:"failure_threshold,omitempty"`
	// RecoveryThreshold indicates the number of consecutive successful probes required to
	// mark an unavailable target as available
	RecoveryThreshold int `yaml:"recovery_threshold,omitempty"`

	// Target Outbound Request Options
	// Verb provides the HTTP verb to use when making an upstream health check
	Verb string `yaml:"verb,omitempty"`
	// Scheme is the scheme to use when making an upstream health check (http or https)
	Scheme string `yaml:"scheme,omitempty"`
	// Host is the Host name header to use when making an upstream health check
	Host string `yaml:"host,omitempty"`
	// Path provides the URL path for the upstream health check
	Path string `yaml:"path,omitempty"`
	// Query provides the HTTP query parameters to use when making an upstream health check
	Query string `yaml:"query,omitempty"`
	// Headers provides the HTTP Headers to apply when making an upstream health check
	Headers map[string]string `yaml:"headers,omitempty"`
	// Body provides a body to apply when making an upstream health check request
	Body string `yaml:"body,omitempty"`
	// TimeoutMS is the amount of time a health check probe should wait for a response
	// before timing out
	TimeoutMS int `yaml:"timeout_ms,omitempty"`
	// Target Probe Response Options
	// ExpectedCodes is the list of Status Codes that positively indicate a Healthy status
	ExpectedCodes []int `yaml:"expected_codes,omitempty"`
	// ExpectedHeaders is a list of Headers (name and value) expected in the response
	// in order to be considered Healthy status
	ExpectedHeaders map[string]string `yaml:"expected_headers,omitempty"`
	// ExpectedBody is the body expected in the response to be considered Healthy status
	ExpectedBody string `yaml:"expected_body,omitempty"`

	md              yamlx.KeyLookup
	hasExpectedBody bool
}

// New returns a new Options reference with default values
func New() *Options {
	return &Options{
		Verb:              DefaultHealthCheckVerb,
		Scheme:            "http",
		Headers:           make(map[string]string),
		Path:              DefaultHealthCheckPath,
		Query:             DefaultHealthCheckQuery,
		ExpectedCodes:     []int{200},
		FailureThreshold:  DefaultHealthCheckFailureThreshold,
		RecoveryThreshold: DefaultHealthCheckRecoveryThreshold,
	}
}

// SetMetaData sets the metadata for the health checker options
func (o *Options) SetMetaData(md yamlx.KeyLookup) {
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
		copy(c.ExpectedCodes, o.ExpectedCodes)
	}
	c.md = o.md
	c.hasExpectedBody = o.hasExpectedBody
	return c
}

func (o *Options) Overlay(name string, custom *Options) {
	if custom == nil || custom.md == nil {
		return
	}
	if custom.md.IsDefined("backends", name, "healthcheck", "path") {
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
		ms = DefaultHealthCheckTimeoutMS
	} else if ms < MinProbeWaitMS {
		ms = MinProbeWaitMS
	}
	return time.Duration(ms) * time.Millisecond
}
