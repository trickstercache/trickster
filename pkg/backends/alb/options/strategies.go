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
package options

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// DefaultIPv6Prefix is how many leading bits of an IPv6 client address form its key.
	DefaultIPv6Prefix = 64
	// LTSignalFirstWrite samples latency at the first byte written to an HTTP client.
	LTSignalFirstWrite = "first_write"
	// LTSignalConnect samples the time to connect to the member; tcp and tls listeners.
	LTSignalConnect = "connect"
	// LTSignalFirstByte samples the time to the member's first byte; tcp and tls listeners.
	LTSignalFirstByte = "first_byte"
	// LTSignalFirstReply samples the time to the member's first datagram; udp listeners.
	LTSignalFirstReply = "first_reply"
	// MaxConnectRetries bounds stream.connect_retries.
	MaxConnectRetries = 10
	// DefaultPassiveFailures is how many consecutive connect failures eject a member.
	DefaultPassiveFailures = 3
)

// ltSignals are the latency signals by the listener protocols that can sample them; the
// empty signal is each protocol's first
var ltSignals = map[string][]string{
	"http": {LTSignalFirstWrite},
	"tcp":  {LTSignalConnect, LTSignalFirstByte},
	"tls":  {LTSignalConnect, LTSignalFirstByte},
	"udp":  {LTSignalFirstReply},
}

// LTSignalFor returns the latency signal in effect on a listener of the given protocol, or
// an error when the configured one cannot be sampled there.
func (o *Options) LTSignalFor(protocol string) (string, error) {
	valid, ok := ltSignals[protocol]
	if !ok {
		valid = ltSignals["http"]
	}
	if o.LT.Signal == "" {
		return valid[0], nil
	}
	if !slices.Contains(valid, o.LT.Signal) {
		return "", fmt.Errorf("%w: %q on a %s listener (use %s)", ErrInvalidLTSignal, o.LT.Signal,
			protocol, strings.Join(valid, " or "))
	}
	return o.LT.Signal, nil
}

// StreamOptions configure how an ALB balances tcp, tls and udp flows.
type StreamOptions struct {
	// ConnectRetries is how many other pool members a tcp or tls connection may be offered
	// when it cannot connect to the one it was given, all within the listener's connect
	// timeout. The default, 0, refuses the connection instead.
	ConnectRetries int `yaml:"connect_retries,omitempty"`
	// PassiveHealth takes a member out of the pool when connections keep failing to reach it,
	// without waiting for a health check. Off unless set.
	PassiveHealth *PassiveHealthOptions `yaml:"passive_health,omitempty"`
}

// PassiveHealthOptions configure passive ejection.
type PassiveHealthOptions struct {
	// Failures is how many consecutive failed connects eject a member; the default is 3.
	Failures int `yaml:"failures,omitempty"`
	// Eject is how long an ejected member stays out; the default is 30s.
	Eject timeconv.Duration `yaml:"eject,omitempty"`
	// MaxEjectedPercent is the most of the pool that may be ejected at once; the default is
	// 50. The last live member is never ejected.
	MaxEjectedPercent int `yaml:"max_ejected_percent,omitempty"`
}

var (
	// ErrInvalidConnectRetries is returned for a stream.connect_retries outside 0-10.
	ErrInvalidConnectRetries = errors.New("'stream.connect_retries' must be between 0 and 10")
	// ErrInvalidPassiveHealth is returned for a negative or out-of-range passive_health value.
	ErrInvalidPassiveHealth = errors.New("'stream.passive_health' values cannot be negative, " +
		"and 'max_ejected_percent' cannot exceed 100")
)

func (o *StreamOptions) validate() error {
	if o == nil {
		return nil
	}
	if o.ConnectRetries < 0 || o.ConnectRetries > MaxConnectRetries {
		return ErrInvalidConnectRetries
	}
	if p := o.PassiveHealth; p != nil &&
		(p.Failures < 0 || p.Eject < 0 || p.MaxEjectedPercent < 0 || p.MaxEjectedPercent > 100) {
		return ErrInvalidPassiveHealth
	}
	return nil
}

// Clone returns a deep copy of the options.
func (o *StreamOptions) Clone() *StreamOptions {
	if o == nil {
		return nil
	}
	c := *o
	if o.PassiveHealth != nil {
		p := *o.PassiveHealth
		c.PassiveHealth = &p
	}
	return &c
}

var (
	// ErrHRWOnlyForHRW is returned when the hrw block is set for another mechanism.
	ErrHRWOnlyForHRW = errors.New("'hrw' options are only valid for mechanism 'hrw'")
	// ErrLTOnlyForLT is returned when the lt block is set for another mechanism.
	ErrLTOnlyForLT = errors.New("'lt' options are only valid for mechanism 'lt'")
	// ErrInvalidIPv6Prefix is returned for an hrw.ipv6_prefix outside 1-128.
	ErrInvalidIPv6Prefix = errors.New("'hrw.ipv6_prefix' must be between 1 and 128")
	// ErrInvalidLTSignal is returned for an lt.signal the listener's protocol cannot sample.
	ErrInvalidLTSignal = errors.New("value for 'lt.signal' is invalid")
	// ErrInvalidLTDecay is returned for a negative lt.decay.
	ErrInvalidLTDecay = errors.New("'lt.decay' cannot be negative")
)

// HRWOptions configures the highest random weight mechanism.
type HRWOptions struct {
	// Key is what a client's affinity follows: client_ip (the default), host,
	// header:<name>, cookie:<name> or query:<name>.
	Key string `yaml:"key,omitempty"`
	// IPv6Prefix is how many leading bits of an IPv6 client address form a client_ip key.
	// The default, 64, keeps a client that rotates its privacy address on one member.
	IPv6Prefix int `yaml:"ipv6_prefix,omitempty"`
	// KeySource is Key, parsed
	KeySource KeySource `yaml:"-"`
}

// LTOptions configures the least time mechanism.
type LTOptions struct {
	// StatusCodes are the response codes that count as a good answer, as bare codes or
	// inclusive {start, end} ranges; any other records a latency penalty instead of a sample.
	// The default is every code but 502, 503 and 504.
	StatusCodes types.StatusRanges `yaml:"status_codes,omitempty"`
	// Decay is the time constant of a member's latency average; the default is 10s.
	Decay timeconv.Duration `yaml:"decay,omitempty"`
	// Signal is what is timed. The default is the listener protocol's own: first_write on
	// http, connect on tcp and tls (or first_byte), first_reply on udp.
	Signal string `yaml:"signal,omitempty"`
	// GoodCodes is StatusCodes, compiled
	GoodCodes *types.StatusTable `yaml:"-"`
}

// DefaultLTStatusCodes returns the response codes lt counts as a good answer when none are
// configured: all but the gateway failures.
func DefaultLTStatusCodes() types.StatusRanges {
	return types.StatusRanges{{Start: 100, End: 501}, {Start: 505, End: 599}}
}

func (o HRWOptions) isZero() bool {
	return o.Key == "" && o.IPv6Prefix == 0
}

func (o LTOptions) isZero() bool {
	return len(o.StatusCodes) == 0 && o.Decay == 0 && o.Signal == ""
}

// initializeStrategies fills the defaults of the block that belongs to the mechanism
func (o *Options) initializeStrategies() error {
	switch o.MechanismName {
	case names.MechanismHRW, names.MechanismHighestRandomWeight:
		ks, err := ParseKeySource(o.HRW.Key)
		if err != nil {
			return fmt.Errorf("hrw.key: %w", err)
		}
		o.HRW.KeySource = ks
		if o.HRW.IPv6Prefix == 0 {
			o.HRW.IPv6Prefix = DefaultIPv6Prefix
		}
	case names.MechanismLT, names.MechanismLeastTime:
		codes := o.LT.StatusCodes
		if len(codes) == 0 {
			codes = DefaultLTStatusCodes()
		}
		o.LT.GoodCodes = codes.Compile()
	}
	if o.Stream != nil && o.Stream.PassiveHealth != nil && o.Stream.PassiveHealth.Failures == 0 {
		o.Stream.PassiveHealth.Failures = DefaultPassiveFailures
	}
	return nil
}

func (o *Options) validateStrategies() error {
	if err := o.Stream.validate(); err != nil {
		return err
	}
	switch o.MechanismName {
	case names.MechanismHRW, names.MechanismHighestRandomWeight:
		if !o.LT.isZero() {
			return ErrLTOnlyForLT
		}
		if _, err := ParseKeySource(o.HRW.Key); err != nil {
			return fmt.Errorf("hrw.key: %w", err)
		}
		if o.HRW.IPv6Prefix < 0 || o.HRW.IPv6Prefix > 128 {
			return ErrInvalidIPv6Prefix
		}
	case names.MechanismLT, names.MechanismLeastTime:
		if !o.HRW.isZero() {
			return ErrHRWOnlyForHRW
		}
		if err := o.LT.StatusCodes.Validate(); err != nil {
			return fmt.Errorf("lt.status_codes: %w", err)
		}
		if o.LT.Decay < 0 {
			return ErrInvalidLTDecay
		}
		known := false
		for _, signals := range ltSignals {
			known = known || slices.Contains(signals, o.LT.Signal)
		}
		if o.LT.Signal != "" && !known {
			return fmt.Errorf("%w: %q", ErrInvalidLTSignal, o.LT.Signal)
		}
	default:
		if !o.HRW.isZero() {
			return ErrHRWOnlyForHRW
		}
		if !o.LT.isZero() {
			return ErrLTOnlyForLT
		}
	}
	return nil
}

// LTDecay returns the configured lt.decay, or zero for the default.
func (o *Options) LTDecay() time.Duration {
	return time.Duration(o.LT.Decay)
}
