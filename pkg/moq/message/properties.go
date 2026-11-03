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

const (
	greaseBase = 0x9d
	greaseStep = 0x7f
)

var (
	// ErrNestedImmutable is returned when Immutable Properties contain Immutable Properties
	ErrNestedImmutable = errors.New("moq: immutable properties nest")
	// ErrPropertyValue is returned when a known property's value is outside its allowed range
	ErrPropertyValue = errors.New("moq: property value out of range")
	// ErrPropertyScope is returned for a known property outside its scope, such as a track property on an object
	ErrPropertyScope = errors.New("moq: property outside its scope")
	// ErrPropertyRepeated is returned for a known property that appears more than once
	ErrPropertyRepeated = errors.New("moq: property repeated")
)

// propertyScope is where a property this implementation understands may appear, and its bit for repetition checks
type propertyScope struct {
	track, object bool
	bit           uint16
}

func scopeOf(t uint64) (propertyScope, bool) {
	switch t {
	case PropObjectDeliveryTimeout:
		return propertyScope{true, true, 1 << 0}, true
	case PropMaxCacheDuration:
		return propertyScope{true, false, 1 << 1}, true
	case PropSubgroupDeliveryTimeout:
		return propertyScope{true, true, 1 << 2}, true
	case PropImmutable:
		return propertyScope{true, true, 1 << 3}, true
	case PropDefaultPublisherPriority:
		return propertyScope{true, false, 1 << 4}, true
	case PropDefaultGroupOrder:
		return propertyScope{true, false, 1 << 5}, true
	case PropDynamicGroups:
		return propertyScope{true, false, 1 << 6}, true
	case PropPriorGroupIDGap:
		return propertyScope{false, true, 1 << 7}, true
	case PropPriorObjectIDGap:
		return propertyScope{false, true, 1 << 8}, true
	}
	return propertyScope{}, false
}

// CheckObject reports a Mandatory Track Property among an object's properties, which makes its track malformed
func (p Properties) CheckObject() error {
	if len(p) == 0 {
		return nil
	}
	if _, ok := p.Mandatory(); ok {
		return ErrPropertyScope
	}
	return nil
}

// CheckScope reports a property this implementation understands that appears outside its scope, track or object,
// or more than once counting Immutable Properties, either of which makes the track malformed (from draft -23)
func (p Properties) CheckScope(track bool) error {
	if len(p) == 0 {
		return nil
	}
	var seen uint16
	check := func(t uint64) error {
		if !track && isMandatory(t) {
			return ErrPropertyScope
		}
		sc, known := scopeOf(t)
		switch {
		case !known:
			return nil
		case track && !sc.track, !track && !sc.object:
			return ErrPropertyScope
		case seen&sc.bit != 0:
			return ErrPropertyRepeated
		}
		seen |= sc.bit
		return nil
	}
	it := wire.NewKVIter(p)
	for kv, ok := it.Next(); ok; kv, ok = it.Next() {
		if err := check(kv.Type); err != nil {
			return err
		}
		if kv.Type != PropImmutable {
			continue
		}
		inner := wire.NewKVIter(kv.Bytes)
		for ikv, ok := inner.Next(); ok; ikv, ok = inner.Next() {
			if err := check(ikv.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

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
	var first uint64
	found := false
	p.eachMandatory(func(t uint64) bool {
		first, found = t, true
		return false
	})
	return first, found
}

// MandatoryTypes returns every Mandatory Track Property type present, for UNSUPPORTED_PROPERTY Error Details
func (p Properties) MandatoryTypes() []uint64 {
	var out []uint64
	p.eachMandatory(func(t uint64) bool {
		out = append(out, t)
		return true
	})
	return out
}

func (p Properties) eachMandatory(fn func(uint64) bool) {
	it := wire.NewKVIter(p)
	for kv, ok := it.Next(); ok; kv, ok = it.Next() {
		if isMandatory(kv.Type) && !fn(kv.Type) {
			return
		}
		if kv.Type == PropImmutable {
			inner := wire.NewKVIter(kv.Bytes)
			for ikv, ok := inner.Next(); ok; ikv, ok = inner.Next() {
				if isMandatory(ikv.Type) && !fn(ikv.Type) {
					return
				}
			}
		}
	}
}

func isMandatory(t uint64) bool {
	// grease values in the range are not Mandatory Track Properties
	return t >= PropMandatoryMin && t <= PropMandatoryMax && !IsGrease(t)
}

// IsGrease reports whether v is a value reserved for greasing, 0x7f * N + 0x9D
func IsGrease(v uint64) bool {
	return v >= greaseBase && (v-greaseBase)%greaseStep == 0
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
