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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

const (
	// DefaultIPv6Prefix is how many leading bits of an IPv6 client address form its key.
	DefaultIPv6Prefix = 64
	// LTSignalFirstWrite samples latency at the first byte written to the client.
	LTSignalFirstWrite = "first_write"
)

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
	// Signal is what is timed; first_write, the default and the only signal of an HTTP
	// listener, is the first byte written to the client.
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
		if o.LT.Signal == "" {
			o.LT.Signal = LTSignalFirstWrite
		}
	}
	return nil
}

func (o *Options) validateStrategies() error {
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
		if o.LT.Signal != "" && o.LT.Signal != LTSignalFirstWrite {
			return fmt.Errorf("%w: %q (use %s)", ErrInvalidLTSignal, o.LT.Signal, LTSignalFirstWrite)
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
