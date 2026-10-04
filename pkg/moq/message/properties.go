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

package message

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// Property types defined by MoQ Transport
const (
	PropObjectDeliveryTimeout    uint64 = 0x02
	PropMaxCacheDuration         uint64 = 0x04
	PropSubgroupDeliveryTimeout  uint64 = 0x06
	PropImmutable                uint64 = 0x0b
	PropDefaultPublisherPriority uint64 = 0x0e
	PropDefaultGroupOrder        uint64 = 0x22
	PropDynamicGroups            uint64 = 0x30
	PropPriorGroupIDGap          uint64 = 0x3c
	PropPriorObjectIDGap         uint64 = 0x3e
	// PropMandatoryMin and PropMandatoryMax bound the Mandatory Track Properties, which an endpoint must
	// understand before it processes or forwards the track
	PropMandatoryMin uint64 = 0x4000
	PropMandatoryMax uint64 = 0x7fff
)

// DefaultPublisherPriority applies when a track sets no DEFAULT_PUBLISHER_PRIORITY
const DefaultPublisherPriority = 128

var (
	// ErrNestedImmutable is returned when Immutable Properties contain Immutable Properties
	ErrNestedImmutable = errors.New("moq: immutable properties nest")
	// ErrPropertyValue is returned when a known property's value is outside its allowed range
	ErrPropertyValue = errors.New("moq: property value out of range")
)

// Properties is a block of Key-Value-Pair properties kept exactly as received, so a relay caches and forwards
// them unchanged; lookups decode lazily and search inside Immutable Properties as well
type Properties []byte

// Find returns the first property of type t among the mutable properties, then inside Immutable Properties
func (p Properties) Find(t uint64) (wire.KV, bool) {
	if kv, ok := wire.FindKV(p, t); ok {
		return kv, true
	}
	if t == PropImmutable {
		return wire.KV{}, false
	}
	if imm, ok := wire.FindKV(p, PropImmutable); ok {
		return wire.FindKV(imm.Bytes, t)
	}
	return wire.KV{}, false
}

// Uint returns the varint value of the property of even type t
func (p Properties) Uint(t uint64) (uint64, bool) {
	kv, ok := p.Find(t)
	return kv.Uint, ok
}

// Validate checks the framing of the block and of any Immutable Properties within it, and the ranges of the
// track properties this implementation understands
func (p Properties) Validate() error {
	it := wire.NewKVIter(p)
	for {
		kv, ok := it.Next()
		if !ok {
			return it.Err()
		}
		if err := validateProperty(kv, true); err != nil {
			return err
		}
	}
}

func validateProperty(kv wire.KV, top bool) error {
	switch kv.Type {
	case PropImmutable:
		if !top {
			return ErrNestedImmutable
		}
		inner := wire.NewKVIter(kv.Bytes)
		for {
			ikv, ok := inner.Next()
			if !ok {
				return inner.Err()
			}
			if err := validateProperty(ikv, false); err != nil {
				return err
			}
		}
	case PropDefaultPublisherPriority:
		if kv.Uint > 255 {
			return ErrPropertyValue
		}
	case PropDefaultGroupOrder:
		if kv.Uint != uint64(GroupOrderAscending) && kv.Uint != uint64(GroupOrderDescending) {
			return ErrPropertyValue
		}
	case PropDynamicGroups:
		if kv.Uint > 1 {
			return ErrPropertyValue
		}
	}
	return nil
}

// Mandatory returns the first Mandatory Track Property type present, which this implementation does not
// understand, so the track must be refused rather than processed or forwarded
func (p Properties) Mandatory() (uint64, bool) {
	it := wire.NewKVIter(p)
	for kv, ok := it.Next(); ok; kv, ok = it.Next() {
		if kv.Type >= PropMandatoryMin && kv.Type <= PropMandatoryMax {
			return kv.Type, true
		}
		if kv.Type == PropImmutable {
			inner := wire.NewKVIter(kv.Bytes)
			for ikv, ok := inner.Next(); ok; ikv, ok = inner.Next() {
				if ikv.Type >= PropMandatoryMin && ikv.Type <= PropMandatoryMax {
					return ikv.Type, true
				}
			}
		}
	}
	return 0, false
}

// PublisherPriority returns DEFAULT_PUBLISHER_PRIORITY, or the default of 128
func (p Properties) PublisherPriority() uint8 {
	if v, ok := p.Uint(PropDefaultPublisherPriority); ok && v <= 255 {
		return uint8(v) // #nosec G115 -- bounded by the check above
	}
	return DefaultPublisherPriority
}

// GroupOrder returns DEFAULT_PUBLISHER_GROUP_ORDER, or Ascending
func (p Properties) GroupOrder() GroupOrder {
	if v, ok := p.Uint(PropDefaultGroupOrder); ok &&
		(v == uint64(GroupOrderAscending) || v == uint64(GroupOrderDescending)) {
		return GroupOrder(v) // #nosec G115 -- one of two small values, checked above
	}
	return GroupOrderAscending
}

// MaxCacheDuration returns MAX_CACHE_DURATION in milliseconds, and false when the track sets none
func (p Properties) MaxCacheDuration() (uint64, bool) {
	return p.Uint(PropMaxCacheDuration)
}

// DynamicGroups reports whether the track lets subscribers request a new group
func (p Properties) DynamicGroups() bool {
	v, ok := p.Uint(PropDynamicGroups)
	return ok && v == 1
}
