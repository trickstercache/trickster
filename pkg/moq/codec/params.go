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

package codec

import (
	"errors"
	"fmt"
	"math"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	maxLocationFilterFields = 4
	reasonUnknownParam      = "unknown parameter"
)

var (
	// ErrParamNotAllowed is returned when encoding a parameter the message or draft does not carry
	ErrParamNotAllowed = errors.New("moq: parameter not allowed in this message")
	// ErrLocationFilter is returned when encoding a Location Filter the draft cannot express
	ErrLocationFilter = errors.New("moq: location filter not expressible in this draft")
)

// KnownParams returns the parameters the draft defines
func (c *Codec) KnownParams() message.ParamSet {
	s := message.Of(message.ParamObjectDeliveryTimeout, message.ParamAuthorizationToken,
		message.ParamRendezvousTimeout, message.ParamExpires, message.ParamLargestObject, message.ParamForward,
		message.ParamSubscriberPriority, message.ParamLocationFilter, message.ParamGroupOrder,
		message.ParamNewGroupRequest, message.ParamSubgroupDeliveryTimeout, message.ParamFillTimeout,
		message.ParamTrackNamespacePrefix)
	if c.Has(CapRangeFilters) {
		s |= message.Of(message.ParamSubgroupFilter, message.ParamObjectIDFilter, message.ParamPriorityFilter,
			message.ParamObjectPropertyFilter, message.ParamTrackPropertyFilter)
	}
	if c.Has(CapFillFetch) {
		s |= message.Of(message.ParamFillParameters, message.ParamIncludeProperties)
	}
	return s
}

func (c *Codec) putVarint(b []byte, v uint64) []byte {
	if c.Has(CapSevenByteVarint) {
		return wire.AppendVarint(b, v)
	}
	return wire.AppendVarintNo7(b, v)
}

func (c *Codec) varintLen(v uint64) int {
	n := wire.VarintLen(v)
	if n == 7 && !c.Has(CapSevenByteVarint) {
		return 8
	}
	return n
}

func (c *Codec) putBytes(b, p []byte) []byte {
	return append(c.putVarint(b, uint64(len(p))), p...)
}

func (c *Codec) putString(b []byte, s string) []byte {
	return append(c.putVarint(b, uint64(len(s))), s...)
}

func (c *Codec) putNamespace(b []byte, ns wire.Namespace) []byte {
	b = c.putVarint(b, uint64(len(ns)))
	for _, f := range ns {
		b = c.putBytes(b, f)
	}
	return b
}

func (c *Codec) putLocation(b []byte, l wire.Location) []byte {
	return c.putVarint(c.putVarint(b, l.Group), l.Object)
}

func (c *Codec) prefixLength(b []byte, start int) []byte {
	// the value was appended first; its varint length is inserted ahead of it at start
	n := uint64(len(b) - start) // #nosec G115 -- a length is never negative
	ln := c.varintLen(n)
	for range ln {
		b = append(b, 0)
	}
	copy(b[start+ln:], b[start:len(b)-ln])
	var tmp [wire.MaxVarintLen]byte
	copy(b[start:start+ln], c.putVarint(tmp[:0], n))
	return b
}

func paramCount(p *message.Params) uint64 {
	n := uint64(len(p.AuthTokens) + len(p.RangeFilters))
	for s := p.Set; s != 0; s &= s - 1 {
		n++
	}
	return n
}

func (c *Codec) appendParams(b []byte, p *message.Params, allowed message.ParamSet) ([]byte, error) {
	// a count, then each parameter in ascending type order with its type a delta from the one before
	if p.Present()&^(allowed&c.KnownParams()) != 0 {
		return b, ErrParamNotAllowed
	}
	b = c.putVarint(b, paramCount(p))
	var prev uint64
	var err error
	put := func(t message.ParamType) {
		b = c.putVarint(b, uint64(t)-prev)
		prev = uint64(t)
	}
	for _, t := range message.ParamTypes {
		switch t {
		case message.ParamAuthorizationToken:
			for _, tok := range p.AuthTokens {
				put(t)
				start := len(b)
				if b, err = wire.AppendToken(b, tok); err != nil {
					return b, err
				}
				b = c.prefixLength(b, start)
			}
			continue
		case message.ParamSubgroupFilter, message.ParamObjectIDFilter, message.ParamPriorityFilter,
			message.ParamObjectPropertyFilter, message.ParamTrackPropertyFilter:
			for i := range p.RangeFilters {
				if p.RangeFilters[i].Type != t {
					continue
				}
				put(t)
				if b, err = c.appendRangeFilter(b, &p.RangeFilters[i]); err != nil {
					return b, err
				}
			}
			continue
		}
		if !p.Set.Has(t) {
			continue
		}
		put(t)
		switch t {
		case message.ParamObjectDeliveryTimeout:
			b = c.putVarint(b, p.ObjectDeliveryTimeout)
		case message.ParamSubgroupDeliveryTimeout:
			b = c.putVarint(b, p.SubgroupDeliveryTimeout)
		case message.ParamRendezvousTimeout:
			b = c.putVarint(b, p.RendezvousTimeout)
		case message.ParamExpires:
			b = c.putVarint(b, p.Expires)
		case message.ParamFillTimeout:
			b = c.putVarint(b, p.FillTimeout)
		case message.ParamNewGroupRequest:
			b = c.putVarint(b, p.NewGroupRequest)
		case message.ParamLargestObject:
			b = c.putLocation(b, p.LargestObject)
		case message.ParamForward:
			b = append(b, boolByte(p.Forward))
		case message.ParamIncludeProperties:
			b = append(b, boolByte(p.IncludeProperties))
		case message.ParamSubscriberPriority:
			b = append(b, p.SubscriberPriority)
		case message.ParamGroupOrder:
			b = append(b, byte(p.GroupOrder))
		case message.ParamLocationFilter:
			if b, err = c.appendLocationFilter(b, p.LocationFilter); err != nil {
				return b, err
			}
		case message.ParamTrackNamespacePrefix:
			b = c.putNamespace(b, p.TrackNamespacePrefix)
		case message.ParamFillParameters:
			fill := p.Fill
			if fill == nil {
				fill = &message.Params{}
			}
			start := len(b)
			if b, err = c.appendParams(b, fill, message.FillParams); err != nil {
				return b, err
			}
			b = c.prefixLength(b, start)
		}
	}
	return b, nil
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func (c *Codec) appendLocationFilter(b []byte, f message.LocationFilter) ([]byte, error) {
	if c.Has(CapTypedLocationFilter) {
		if f.Type > message.FilterNextObject {
			return b, ErrLocationFilter
		}
		b = c.putVarint(b, uint64(f.Type))
		return appendFilterFields(c, b, f), nil
	}
	// a length-prefixed list whose field count gives the form; {0, 0} as two fields means the Next Object
	start := len(b)
	switch f.Type {
	case message.FilterNone:
	case message.FilterNextObject:
		b = append(b, 0, 0)
	case message.FilterAbsoluteStart:
		// an open-ended filter from {0, 0} is unfiltered, and its two-field spelling means Next Object here
		if f.StartGroup != 0 || f.StartObject != 0 {
			b = appendFilterFields(c, b, f)
		}
	case message.FilterRelativeStart, message.FilterAbsoluteStartGroupEnd, message.FilterAbsoluteRange:
		b = appendFilterFields(c, b, f)
	default:
		return b, ErrLocationFilter
	}
	return c.prefixLength(b, start), nil
}

func appendFilterFields(c *Codec, b []byte, f message.LocationFilter) []byte {
	switch f.Type {
	case message.FilterRelativeStart:
		b = c.putVarint(b, f.StartGroup)
	case message.FilterAbsoluteStart:
		b = c.putVarint(c.putVarint(b, f.StartGroup), f.StartObject)
	case message.FilterAbsoluteStartGroupEnd:
		b = c.putVarint(c.putVarint(c.putVarint(b, f.StartGroup), f.StartObject), f.EndGroupDelta)
	case message.FilterAbsoluteRange:
		b = c.putVarint(c.putVarint(c.putVarint(b, f.StartGroup), f.StartObject), f.EndGroupDelta)
		b = c.putVarint(b, f.EndObject)
	}
	return b
}

func hasPropertyType(t message.ParamType) bool {
	return t == message.ParamObjectPropertyFilter || t == message.ParamTrackPropertyFilter
}

func (c *Codec) appendRangeFilter(b []byte, f *message.RangeFilter) ([]byte, error) {
	if f.Remove {
		return c.putVarint(b, 0), nil
	}
	start := len(b)
	b = append(b, f.SetID)
	if hasPropertyType(f.Type) {
		b = c.putVarint(b, f.PropertyType)
	}
	var err error
	if b, err = wire.AppendRanges(b, f.Ranges); err != nil {
		return b, err
	}
	return c.prefixLength(b, start), nil
}

func (c *Codec) parseParams(r *wire.Reader, allowed message.ParamSet) (message.Params, error) {
	// unknown parameters cannot be skipped, so any the message or draft does not carry ends the session
	var p message.Params
	allowed &= c.KnownParams()
	n := r.Count()
	var prev uint64
	for i := range n {
		d := r.Varint()
		if r.Err() != nil {
			break
		}
		if d > math.MaxUint64-prev {
			return p, message.Violation("parameter type overflows")
		}
		t := message.ParamType(prev + d)
		if i > 0 && d == 0 && !message.Repeatable(t) {
			return p, message.Violation(fmt.Sprintf("duplicate parameter %#x", uint64(t)))
		}
		prev = uint64(t)
		if message.Bit(t) == 0 {
			return p, message.Violation(reasonUnknownParam)
		}
		if !allowed.Has(t) {
			return p, message.Violation(fmt.Sprintf("parameter %#x not allowed here", uint64(t)))
		}
		if err := c.parseParam(r, t, &p); err != nil {
			return p, err
		}
	}
	if err := r.Err(); err != nil {
		return p, wireViolation(err)
	}
	return p, nil
}

func (c *Codec) parseParam(r *wire.Reader, t message.ParamType, p *message.Params) error {
	switch t {
	case message.ParamObjectDeliveryTimeout:
		p.ObjectDeliveryTimeout = r.Varint()
	case message.ParamSubgroupDeliveryTimeout:
		p.SubgroupDeliveryTimeout = r.Varint()
	case message.ParamRendezvousTimeout:
		p.RendezvousTimeout = r.Varint()
	case message.ParamExpires:
		p.Expires = r.Varint()
	case message.ParamFillTimeout:
		p.FillTimeout = r.Varint()
	case message.ParamNewGroupRequest:
		p.NewGroupRequest = r.Varint()
	case message.ParamLargestObject:
		p.LargestObject = r.Location()
	case message.ParamSubscriberPriority:
		p.SubscriberPriority = r.Uint8()
	case message.ParamForward, message.ParamIncludeProperties:
		v := r.Uint8()
		if v > 1 {
			return message.Violation("boolean parameter out of range")
		}
		if t == message.ParamForward {
			p.Forward = v == 1
		} else {
			p.IncludeProperties = v == 1
		}
	case message.ParamGroupOrder:
		p.GroupOrder = message.GroupOrder(r.Uint8())
		if r.Err() == nil && !p.GroupOrder.Valid() {
			return message.Violation("group order out of range")
		}
	case message.ParamTrackNamespacePrefix:
		p.TrackNamespacePrefix = r.Namespace()
	case message.ParamLocationFilter:
		f, err := c.parseLocationFilter(r)
		if err != nil {
			return err
		}
		p.LocationFilter = f
	case message.ParamAuthorizationToken:
		v := r.LenBytes(wire.MaxMessageLen)
		if r.Err() != nil {
			return nil
		}
		tok, err := wire.ParseToken(v)
		if err != nil {
			return &message.SessionError{Code: message.CodeKeyValueFormatting, Reason: err.Error()}
		}
		p.AuthTokens = append(p.AuthTokens, tok)
		return nil
	case message.ParamFillParameters:
		v := r.LenBytes(wire.MaxMessageLen)
		if r.Err() != nil {
			return nil
		}
		sub := wire.NewReader(v)
		fill, err := c.parseParams(&sub, message.FillParams)
		if err != nil {
			return err
		}
		if err = sub.Done(); err != nil {
			return wireViolation(err)
		}
		p.Fill = &fill
		if fill.Invalid != nil && p.Invalid == nil {
			p.Invalid = fill.Invalid
		}
	default:
		if message.IsRangeFilter(t) {
			c.parseRangeFilter(r, t, p)
			return nil
		}
		return message.Violation(reasonUnknownParam)
	}
	if r.Err() == nil {
		p.Mark(t)
	}
	return nil
}

func (c *Codec) parseLocationFilter(r *wire.Reader) (message.LocationFilter, error) {
	var f message.LocationFilter
	if c.Has(CapTypedLocationFilter) {
		ft := r.Varint()
		if ft > uint64(message.FilterNextObject) {
			if r.Err() == nil {
				return f, message.Violation("unknown location filter type")
			}
			return f, nil
		}
		f.Type = message.FilterType(ft) // #nosec G115 -- bounded by the check above
		switch f.Type {
		case message.FilterNone, message.FilterNextObject:
		case message.FilterRelativeStart:
			f.StartGroup = r.Varint()
		case message.FilterAbsoluteStart:
			f.StartGroup, f.StartObject = r.Varint(), r.Varint()
		case message.FilterAbsoluteStartGroupEnd:
			f.StartGroup, f.StartObject, f.EndGroupDelta = r.Varint(), r.Varint(), r.Varint()
		case message.FilterAbsoluteRange:
			f.StartGroup, f.StartObject, f.EndGroupDelta = r.Varint(), r.Varint(), r.Varint()
			f.EndObject = r.Varint()
		}
		return f, checkFilterEnd(f)
	}
	sub := wire.NewReader(r.LenBytes(wire.MaxMessageLen))
	var fields [maxLocationFilterFields]uint64
	var n int
	for sub.Remaining() > 0 {
		if n == maxLocationFilterFields {
			return f, message.Violation("location filter has too many fields")
		}
		fields[n] = sub.Varint()
		n++
	}
	if err := sub.Err(); err != nil {
		return f, wireViolation(err)
	}
	switch n {
	case 0:
		f.Type = message.FilterNone
	case 1:
		f.Type, f.StartGroup = message.FilterRelativeStart, fields[0]
	case 2:
		f.Type, f.StartGroup, f.StartObject = message.FilterAbsoluteStart, fields[0], fields[1]
		if fields[0] == 0 && fields[1] == 0 {
			f = message.LocationFilter{Type: message.FilterNextObject}
		}
	case 3:
		f.Type = message.FilterAbsoluteStartGroupEnd
		f.StartGroup, f.StartObject, f.EndGroupDelta = fields[0], fields[1], fields[2]
	default:
		f.Type = message.FilterAbsoluteRange
		f.StartGroup, f.StartObject, f.EndGroupDelta, f.EndObject = fields[0], fields[1], fields[2], fields[3]
	}
	return f, checkFilterEnd(f)
}

func checkFilterEnd(f message.LocationFilter) error {
	if (f.Type == message.FilterAbsoluteStartGroupEnd || f.Type == message.FilterAbsoluteRange) &&
		f.EndGroupDelta > math.MaxUint64-f.StartGroup {
		return message.Violation("location filter end group overflows")
	}
	return nil
}

func (c *Codec) parseRangeFilter(r *wire.Reader, t message.ParamType, p *message.Params) {
	v := r.LenBytes(wire.MaxMessageLen)
	if r.Err() != nil {
		return
	}
	f := message.RangeFilter{Type: t}
	if len(v) == 0 {
		f.Remove = true
		p.RangeFilters = append(p.RangeFilters, f)
		return
	}
	invalid := func(reason string) {
		if p.Invalid == nil {
			p.Invalid = &message.RequestFailure{Code: message.RequestInvalidFilter, Reason: reason}
		}
	}
	sub := wire.NewReader(v)
	f.SetID = sub.Uint8()
	if hasPropertyType(t) {
		f.PropertyType = sub.Varint()
		if sub.Err() == nil && wire.IsBytesType(f.PropertyType) {
			invalid("property filters need an integer property type")
		}
	}
	if sub.Err() != nil {
		invalid("range filter truncated")
		return
	}
	rs, err := wire.ParseRanges(sub.Rest())
	if err != nil {
		invalid("range filter bounds overflow or are truncated")
		return
	}
	f.Ranges = rs
	if t == message.ParamPriorityFilter {
		for _, rg := range rs {
			if rg.Start > math.MaxUint8 || (!rg.Open && rg.End > math.MaxUint8) {
				invalid("priority filter bound exceeds 255")
				break
			}
		}
	}
	for i := range p.RangeFilters {
		o := &p.RangeFilters[i]
		if o.Type == t && o.SetID == f.SetID && o.PropertyType == f.PropertyType && !o.Remove {
			invalid("a range filter repeats its type and set")
			break
		}
	}
	p.RangeFilters = append(p.RangeFilters, f)
}

func wireViolation(err error) error {
	// framing errors end the session as a PROTOCOL_VIOLATION unless they already carry a code
	if _, ok := errors.AsType[*message.SessionError](err); ok {
		return err
	}
	return message.Violation(err.Error())
}
