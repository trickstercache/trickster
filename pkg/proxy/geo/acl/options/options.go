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

// Package options defines the geo_acls configuration section: named lists of locations that may, or may not,
// reach the backends and paths that name them.
package options

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	"github.com/trickstercache/trickster/v2/pkg/proxy/clientip"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	locatoropts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"

	"golang.org/x/net/http/httpguts"
)

const (
	// DefaultMessage is what a refused client is told, in every protocol
	DefaultMessage = "This resource is not available in your geographical area."
	// DefaultStatus is the HTTP status of a refusal
	DefaultStatus = http.StatusForbidden
	// ExemptPrivate in exempt stands for every address no database places: loopback, private, link-local,
	// carrier-grade NAT and unique local addresses
	ExemptPrivate = "private"
	// MaxMessageLength is the longest message, in bytes, that every protocol's error form carries
	MaxMessageLength = 255

	minStatus = 400
	maxStatus = 599
)

// Lookup is a map of geo ACL Options keyed by name
type Lookup map[string]*Options

// Options defines a named geo ACL
type Options struct {
	// GeoLocatorName names the locator that places clients; default when unset
	GeoLocatorName string `yaml:"geo_locator_name,omitempty"`
	// Allow lists the only locations allowed; it may not be set with Deny
	Allow []string `yaml:"allow,omitempty"`
	// Deny lists the locations denied; it may not be set with Allow
	Deny []string `yaml:"deny,omitempty"`
	// Unknown judges a client with no location; unset, it is judged as an unlisted location
	Unknown Verdict `yaml:"unknown,omitempty"`
	// Exempt lists addresses and prefixes allowed with no lookup; private stands for every non-routable range
	Exempt []string `yaml:"exempt,omitempty"`
	// Action is what a denial does; unset, it rejects
	Action Action `yaml:"action,omitempty"`
	// Message is what a refused client is told, in every protocol
	Message string `yaml:"message,omitempty"`
	// Response shapes the HTTP refusal
	Response *ResponseOptions `yaml:"response,omitempty"`
	// Name is the geo ACL's name, from its key in the Lookup
	Name string `yaml:"-"`
	// Compiled is the geo ACL built from these Options when the configuration is applied; nil until then
	Compiled Compiled `yaml:"-"`
}

// Compiled is a compiled geo ACL, which the route chain and the native listeners judge clients by
type Compiled interface {
	Name() string
}

// ResponseOptions shape a geo ACL's HTTP refusal
type ResponseOptions struct {
	// Status is the refusal's status code; 403 when unset
	Status int `yaml:"status,omitempty"`
	// Headers are merged over the refusal's default headers
	Headers map[string]string `yaml:"headers,omitempty"`
	// Body replaces the message and its newline as the refusal's body
	Body string `yaml:"body,omitempty"`
}

var (
	// ErrInvalidName is wrapped by the error for an empty or reserved geo ACL name
	ErrInvalidName = errors.New("invalid geo ACL name")
	// ErrOneList is wrapped by the error for a geo ACL with both lists, or neither
	ErrOneList = errors.New("exactly one of 'allow' and 'deny' must have entries")
	// ErrInvalidExempt is wrapped by the error for an exempt entry that is neither an address nor a prefix
	ErrInvalidExempt = errors.New("invalid 'exempt' entry")
	// ErrInvalidMessage is wrapped by the error for a message that no protocol can carry
	ErrInvalidMessage = errors.New("invalid 'message'")
	// ErrInvalidResponse is wrapped by the error for an invalid HTTP response
	ErrInvalidResponse = errors.New("invalid 'response'")
)

// New returns new geo ACL Options
func New() *Options {
	return &Options{}
}

// Clone returns a copy of the Options
func (o *Options) Clone() *Options {
	if o == nil {
		return nil
	}
	out := *o
	out.Allow = slices.Clone(o.Allow)
	out.Deny = slices.Clone(o.Deny)
	out.Exempt = slices.Clone(o.Exempt)
	if o.Response != nil {
		r := *o.Response
		r.Headers = maps.Clone(o.Response.Headers)
		out.Response = &r
	}
	return &out
}

// LocatorName returns the name of the locator the geo ACL uses
func (o *Options) LocatorName() string {
	if o.GeoLocatorName == "" {
		return locatoropts.DefaultName
	}
	return o.GeoLocatorName
}

// IsAllowList reports whether the geo ACL lists the allowed locations, rather than the denied
func (o *Options) IsAllowList() bool {
	return len(o.Allow) > 0
}

// Entries returns the geo ACL's one list
func (o *Options) Entries() []string {
	if o.IsAllowList() {
		return o.Allow
	}
	return o.Deny
}

// EffectiveMessage returns the configured message, or the default
func (o *Options) EffectiveMessage() string {
	if o.Message == "" {
		return DefaultMessage
	}
	return o.Message
}

// EffectiveStatus returns the configured HTTP status, or the default
func (o *Options) EffectiveStatus() int {
	if o.Response == nil || o.Response.Status == 0 {
		return DefaultStatus
	}
	return o.Response.Status
}

// ParseExempt returns whether exempt lists private, and the other entries as a matchable set
func ParseExempt(exempt []string) (bool, clientip.Trusted, error) {
	var private bool
	rest := make([]string, 0, len(exempt))
	for _, e := range exempt {
		if strings.EqualFold(strings.TrimSpace(e), ExemptPrivate) {
			private = true
			continue
		}
		rest = append(rest, e)
	}
	set, err := clientip.ParseTrusted(rest)
	if err != nil {
		return false, nil, fmt.Errorf("%w: %w", ErrInvalidExempt, err)
	}
	return private, set, nil
}

// Validate checks the Options
func (o *Options) Validate() error {
	if o.Name == "" || reserved.IsReference(o.Name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, o.Name)
	}
	if (len(o.Allow) > 0) == (len(o.Deny) > 0) {
		return o.wrap(ErrOneList)
	}
	if _, err := geo.ParseList(o.Entries()); err != nil {
		return o.wrap(err)
	}
	if _, _, err := ParseExempt(o.Exempt); err != nil {
		return o.wrap(err)
	}
	if err := validateMessage(o.Message); err != nil {
		return o.wrap(err)
	}
	if err := o.Response.validate(); err != nil {
		return o.wrap(err)
	}
	return nil
}

func validateMessage(m string) error {
	if m == "" {
		return nil
	}
	if len(m) > MaxMessageLength {
		return fmt.Errorf("%w: longer than %d bytes", ErrInvalidMessage, MaxMessageLength)
	}
	for i := range len(m) {
		if c := m[i]; c < ' ' || c == 0x7f {
			return fmt.Errorf("%w: it must be one line with no control characters", ErrInvalidMessage)
		}
	}
	return nil
}

func (r *ResponseOptions) validate() error {
	if r == nil {
		return nil
	}
	if r.Status != 0 && (r.Status < minStatus || r.Status > maxStatus) {
		return fmt.Errorf("%w: 'status' must be from %d to %d", ErrInvalidResponse, minStatus, maxStatus)
	}
	for name, value := range r.Headers {
		if !httpguts.ValidHeaderFieldName(name) {
			return fmt.Errorf("%w: %q is not a valid header name", ErrInvalidResponse, name)
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			return fmt.Errorf("%w: the %q header's value is not valid", ErrInvalidResponse, name)
		}
	}
	return nil
}

// Warnings returns what is legal in the Options but likely a mistake
func (o *Options) Warnings() []string {
	var out []string
	if o.IsAllowList() {
		if len(o.Exempt) == 0 && o.Unknown == 0 {
			out = append(out, fmt.Sprintf("geo ACL %q has an 'allow' list and no 'exempt' or 'unknown', so it "+
				"denies every client its locator cannot place, private and loopback addresses included", o.Name))
		}
		if o.Unknown == VerdictAllow {
			out = append(out, fmt.Sprintf("geo ACL %q allows every client its locator cannot place "+
				"('unknown: allow' with an 'allow' list)", o.Name))
		}
	}
	if o.EffectiveStatus() == http.StatusUnavailableForLegalReasons && !o.hasBlockedByLink() {
		out = append(out, fmt.Sprintf(`geo ACL %q answers 451 without a 'Link' header with rel="blocked-by", `+
			"which RFC 7725 asks for", o.Name))
	}
	return out
}

func (o *Options) hasBlockedByLink() bool {
	if o.Response == nil {
		return false
	}
	for name, value := range o.Response.Headers {
		if strings.EqualFold(name, "Link") && strings.Contains(strings.ToLower(value), `rel="blocked-by"`) {
			return true
		}
	}
	return false
}

func (o *Options) wrap(err error) error {
	return fmt.Errorf("geo ACL %q: %w", o.Name, err)
}

// Validate validates each Options in the Lookup, naming it by its key first; a nil entry is skipped
func (l Lookup) Validate() error {
	for name, o := range l {
		if o == nil {
			continue
		}
		o.Name = name
		if err := o.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns a copy of the Lookup
func (l Lookup) Clone() Lookup {
	if l == nil {
		return nil
	}
	out := make(Lookup, len(l))
	for name, o := range l {
		out[name] = o.Clone()
	}
	return out
}
