/*
 * Copyright 2026 The Trickster Authors
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

// Package options is the YAML form of a named rate limiter.
package options

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
)

const (
	AlgorithmSlidingWindow = "sliding_window"

	MissingExempt = "exempt"
	MissingShared = "shared"

	ActionReject = "reject"
	ActionCount  = "count"
	ActionClose  = "close"

	OnFullAllow  = "allow"
	OnFullReject = "reject"

	PolicyNone   = "none"
	PolicyIETF   = "ietf"
	PolicyLegacy = "legacy"

	UnitRequests    = "requests"
	UnitConnections = "connections"
	UnitSessions    = "sessions"
	UnitDatagrams   = "datagrams"

	defaultIPv6Prefix = 64
	defaultWindow     = time.Minute
	defaultMaxKeys    = 100000
	defaultStatus     = http.StatusTooManyRequests
	defaultBody       = "Too Many Requests\n"
	maxKeysInLimiter  = 5
	minWindow         = time.Second
	maxWindow         = time.Hour
)

// Response is the HTTP reply a reject action writes. Zero values keep the defaults.
type Response struct {
	Status  int               `yaml:"status,omitempty"`
	Headers map[string]string `yaml:"headers,omitempty"`
	Body    string            `yaml:"body,omitempty"`
}

// Options is one named limiter. Validate fills the compiled fields; Clone shares none of the slices.
type Options struct {
	Name          string            `yaml:"-"`
	Keys          []string          `yaml:"keys,omitempty"`
	IPv6Prefix    int               `yaml:"ipv6_prefix,omitempty"`
	Algorithm     string            `yaml:"algorithm,omitempty"`
	Limit         int64             `yaml:"limit,omitempty"`
	Window        timeconv.Duration `yaml:"window,omitempty"`
	MissingKey    string            `yaml:"missing_key,omitempty"`
	Action        string            `yaml:"action,omitempty"`
	Response      *Response         `yaml:"response,omitempty"`
	PolicyHeaders string            `yaml:"policy_headers,omitempty"`
	MaxKeys       int               `yaml:"max_keys,omitempty"`
	MaxKeysAction string            `yaml:"max_keys_action,omitempty"`
	Unit          string            `yaml:"unit,omitempty"`

	// KeySources is Keys parsed. Empty means one bucket for every event.
	KeySources []flowkey.KeySource `yaml:"-"`
	// Status, Header and Body are the reject response, built once. Header values are clipped.
	Status int         `yaml:"-"`
	Header http.Header `yaml:"-"`
	Body   []byte      `yaml:"-"`
}

// Lookup maps a limiter name to its definition.
type Lookup map[string]*Options

// Clone returns a deep copy. A nil lookup stays nil.
func (l Lookup) Clone() Lookup {
	if l == nil {
		return nil
	}
	out := make(Lookup, len(l))
	for name, options := range l {
		out[name] = options.Clone()
	}
	return out
}

// Clone returns a deep copy of the definition, including the compiled response.
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.Keys = slices.Clone(o.Keys)
	out.KeySources = slices.Clone(o.KeySources)
	out.Body = slices.Clone(o.Body)
	if o.Header != nil {
		out.Header = o.Header.Clone()
	}
	if o.Response != nil {
		resp := *o.Response
		resp.Headers = maps.Clone(o.Response.Headers)
		out.Response = &resp
	}
	return &out
}

// Validate names each definition from its map key and checks the fields in the rate-limit spec.
func (l Lookup) Validate() error {
	for name, options := range l {
		if options == nil || name == "" || reserved.IsReference(name) {
			return fmt.Errorf("invalid rate limiter name %q", name)
		}
		options.Name = name
		if err := options.Validate(); err != nil {
			return fmt.Errorf("rate limiter %q: %w", name, err)
		}
	}
	return nil
}

// Validate applies defaults and checks one limiter. The loader sets Name first.
func (o *Options) Validate() error {
	if o == nil {
		return fmt.Errorf("invalid rate limiter")
	}
	if err := o.normalize(); err != nil {
		return err
	}
	if err := o.compileKeys(); err != nil {
		return err
	}
	return o.compileResponse()
}

func (o *Options) normalize() error {
	if o.Algorithm == "" {
		o.Algorithm = AlgorithmSlidingWindow
	}
	if o.Algorithm != AlgorithmSlidingWindow {
		return fmt.Errorf("algorithm %q is not %s", o.Algorithm, AlgorithmSlidingWindow)
	}
	if o.Limit < 1 || o.Limit > math.MaxUint32 {
		return fmt.Errorf("limit %d is outside 1..%d", o.Limit, uint32(math.MaxUint32))
	}
	if o.Window == 0 {
		o.Window = timeconv.Duration(defaultWindow)
	}
	if time.Duration(o.Window) < minWindow || time.Duration(o.Window) > maxWindow {
		return fmt.Errorf("window %s is outside 1s..1h", time.Duration(o.Window))
	}
	if o.IPv6Prefix == 0 {
		o.IPv6Prefix = defaultIPv6Prefix
	}
	if o.IPv6Prefix < 1 || o.IPv6Prefix > 128 {
		return fmt.Errorf("ipv6_prefix %d is outside 1..128", o.IPv6Prefix)
	}
	if o.MissingKey == "" {
		o.MissingKey = MissingExempt
	}
	if o.MissingKey != MissingExempt && o.MissingKey != MissingShared {
		return fmt.Errorf("missing_key %q is not exempt or shared", o.MissingKey)
	}
	if o.Action == "" {
		o.Action = ActionReject
	}
	if o.Action != ActionReject && o.Action != ActionCount && o.Action != ActionClose {
		return fmt.Errorf("action %q is not reject, count or close", o.Action)
	}
	if o.PolicyHeaders == "" {
		o.PolicyHeaders = PolicyNone
	}
	if o.PolicyHeaders != PolicyNone && o.PolicyHeaders != PolicyIETF && o.PolicyHeaders != PolicyLegacy {
		return fmt.Errorf("policy_headers %q is not none, ietf or legacy", o.PolicyHeaders)
	}
	if o.MaxKeys == 0 {
		o.MaxKeys = defaultMaxKeys
	}
	if o.MaxKeys < 1 {
		return fmt.Errorf("max_keys %d is below 1", o.MaxKeys)
	}
	if o.MaxKeysAction == "" {
		o.MaxKeysAction = OnFullAllow
	}
	if o.MaxKeysAction != OnFullAllow && o.MaxKeysAction != OnFullReject {
		return fmt.Errorf("max_keys_action %q is not allow or reject", o.MaxKeysAction)
	}
	if o.Unit != "" && o.Unit != UnitRequests && o.Unit != UnitConnections &&
		o.Unit != UnitSessions && o.Unit != UnitDatagrams {
		return fmt.Errorf("unit %q is not requests, connections, sessions or datagrams", o.Unit)
	}
	return nil
}

func (o *Options) compileKeys() error {
	if len(o.Keys) > maxKeysInLimiter {
		return fmt.Errorf("keys has %d entries; the maximum is %d", len(o.Keys), maxKeysInLimiter)
	}
	o.KeySources = make([]flowkey.KeySource, len(o.Keys))
	for i, key := range o.Keys {
		parsed, err := flowkey.ParseKeySource(key)
		if err != nil {
			return fmt.Errorf("keys[%d]: %w", i, err)
		}
		o.KeySources[i] = parsed
	}
	return nil
}
