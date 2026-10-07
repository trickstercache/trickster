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

// Package options defines a backend's origin_resolution settings, which
// select how the proxy resolves the origin host it dials.
package options

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

const (
	// ModeA dials the origin host's A/AAAA addresses at the URL's port
	ModeA = "a"
	// ModeSRV dials a target of the SRV records owned by the origin host, at the target's port
	ModeSRV = "srv"

	// ServerNameOwner verifies an https origin against the SRV owner name, which is the URL host
	ServerNameOwner = "owner"
	// ServerNameTarget verifies an https origin against the SRV target it dialed
	ServerNameTarget = "target"

	// DefaultMinTTL is the default floor for a cached answer's lifetime
	DefaultMinTTL = 5 * time.Second
	// DefaultMaxTTL is the default ceiling for a cached answer's lifetime
	DefaultMaxTTL = 60 * time.Second
	// DefaultNegativeTTL is the default lifetime of a cached NXDOMAIN or empty answer
	DefaultNegativeTTL = 5 * time.Second
)

var (
	// ErrInvalidMode is returned for a mode other than a or srv
	ErrInvalidMode = errors.New("'mode' must be 'a' or 'srv'")
	// ErrInvalidResolver is returned when the resolver is not a host:port
	ErrInvalidResolver = errors.New("'resolver' must be a host:port")
	// ErrNegativeTTL is returned when a TTL setting is below zero
	ErrNegativeTTL = errors.New("'min_ttl', 'max_ttl' and 'negative_ttl' must not be negative")
	// ErrTTLFloorAboveCeiling is returned when the effective min_ttl exceeds the effective max_ttl
	ErrTTLFloorAboveCeiling = errors.New("'min_ttl' must not exceed 'max_ttl'")
	// ErrInvalidServerName is returned for a tls_server_name other than owner or target
	ErrInvalidServerName = errors.New("'tls_server_name' must be 'owner' or 'target'")
	// ErrSRVOnly is returned when an srv-only setting is used with mode a
	ErrSRVOnly = errors.New("'resolver', 'min_ttl', 'max_ttl', 'negative_ttl' and " +
		"'tls_server_name' apply only to mode 'srv'")
)

// Options configures how the proxy resolves the origin host it dials
type Options struct {
	// Mode is a (the default: A/AAAA lookup of the URL host at the URL port) or
	// srv (the URL host is an SRV owner name; the port comes from the SRV answer)
	Mode string `yaml:"mode,omitempty"`
	// Resolver is the host:port of a DNS server to query directly, which makes record
	// TTLs visible. When empty, the stdlib resolver is used and min_ttl paces answers.
	Resolver string `yaml:"resolver,omitempty"`
	// MinTTL is the floor for a cached answer's lifetime
	MinTTL timeconv.Duration `yaml:"min_ttl,omitempty"`
	// MaxTTL is the ceiling for a cached answer's lifetime, and how long after its lookup
	// the last good answer may serve while refreshes fail
	MaxTTL timeconv.Duration `yaml:"max_ttl,omitempty"`
	// NegativeTTL is how long an NXDOMAIN or empty answer is cached
	NegativeTTL timeconv.Duration `yaml:"negative_ttl,omitempty"`
	// TLSServerName is owner (the default: the URL host) or target (the dialed SRV target),
	// naming the server an https origin's certificate is verified against
	TLSServerName string `yaml:"tls_server_name,omitempty"`
}

// Clone returns a perfect copy of the Options
func (o *Options) Clone() *Options { return pointers.Clone(o) }

// Initialize normalizes the enumerated values and applies the TTL defaults to an srv mode
func (o *Options) Initialize() {
	if o == nil {
		return
	}
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	o.TLSServerName = strings.ToLower(strings.TrimSpace(o.TLSServerName))
	if !o.IsSRV() {
		return
	}
	o.MinTTL, o.MaxTTL, o.NegativeTTL = o.effectiveTTLs()
}

// IsSRV reports whether the Options select SRV resolution; it tolerates a nil receiver
func (o *Options) IsSRV() bool {
	return o != nil && strings.EqualFold(strings.TrimSpace(o.Mode), ModeSRV)
}

// VerifiesTarget reports whether an https origin is verified against the dialed SRV target
func (o *Options) VerifiesTarget() bool {
	return o.IsSRV() && strings.EqualFold(strings.TrimSpace(o.TLSServerName), ServerNameTarget)
}

// TTLs returns the effective floor, ceiling and negative TTLs, with defaults for unset values
func (o *Options) TTLs() (minTTL, maxTTL, negativeTTL time.Duration) {
	mn, mx, neg := o.effectiveTTLs()
	return time.Duration(mn), time.Duration(mx), time.Duration(neg)
}

func (o *Options) effectiveTTLs() (mn, mx, neg timeconv.Duration) {
	mn, mx, neg = o.MinTTL, o.MaxTTL, o.NegativeTTL
	if mn == 0 {
		mn = timeconv.Duration(DefaultMinTTL)
	}
	if mx == 0 {
		mx = timeconv.Duration(DefaultMaxTTL)
	}
	if neg == 0 {
		neg = timeconv.Duration(DefaultNegativeTTL)
	}
	return mn, mx, neg
}

// Validate validates the Options; it tolerates a nil receiver
func (o *Options) Validate() error {
	if o == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(o.Mode)) {
	case "", ModeA:
		if o.Resolver != "" || o.MinTTL != 0 || o.MaxTTL != 0 || o.NegativeTTL != 0 ||
			o.TLSServerName != "" {
			return ErrSRVOnly
		}
		return nil
	case ModeSRV:
	default:
		return fmt.Errorf("%w, got %q", ErrInvalidMode, o.Mode)
	}
	if o.Resolver != "" {
		host, port, err := net.SplitHostPort(o.Resolver)
		if err != nil || host == "" || port == "" {
			return fmt.Errorf("%w, got %q", ErrInvalidResolver, o.Resolver)
		}
	}
	if o.MinTTL < 0 || o.MaxTTL < 0 || o.NegativeTTL < 0 {
		return ErrNegativeTTL
	}
	if mn, mx, _ := o.effectiveTTLs(); mn > mx {
		return fmt.Errorf("%w (%s > %s)", ErrTTLFloorAboveCeiling,
			time.Duration(mn), time.Duration(mx))
	}
	switch strings.ToLower(strings.TrimSpace(o.TLSServerName)) {
	case "", ServerNameOwner, ServerNameTarget:
	default:
		return fmt.Errorf("%w, got %q", ErrInvalidServerName, o.TLSServerName)
	}
	return nil
}
