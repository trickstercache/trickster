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

// Setup Option types
const (
	OptionPath                  uint64 = 0x01
	OptionAuthorizationToken    uint64 = 0x03
	OptionMaxAuthTokenCacheSize uint64 = 0x04
	OptionAuthority             uint64 = 0x05
	OptionMaxFilterRanges       uint64 = 0x06
	OptionImplementation        uint64 = 0x07
	OptionMaxRequestUpdates     uint64 = 0x08
)

const (
	fetchTypeStandalone      = 0x1 // the Fetch Types of draft -17 to -19 FETCH
	fetchTypeRelativeJoining = 0x2
	fetchTypeAbsoluteJoining = 0x3
)

// ErrUnknownMessage is returned when encoding a message the draft does not define
var ErrUnknownMessage = errors.New("moq: message not defined in this draft")

// AppendMessage appends m framed as a control message: its type, a 16-bit length and its body
func (c *Codec) AppendMessage(b []byte, m message.Message) ([]byte, error) {
	typ, ok := c.MessageType(m.Kind())
	if !ok {
		return b, fmt.Errorf("%w: %s in draft %d", ErrUnknownMessage, m.Kind(), c.draft)
	}
	if err := checkNames(m); err != nil {
		return b, err
	}
	start := len(b)
	b, at := wire.BeginMessage(b, typ)
	var err error
	switch v := m.(type) {
	case *message.Setup:
		b, err = c.appendSetup(b, v)
	case *message.GoAway:
		b = c.putString(b, v.NewSessionURI)
		b = c.putVarint(b, v.Timeout)
		if c.Has(CapGoAwayRequestID) && v.HasRequestID {
			b = c.putVarint(b, v.RequestID)
		}
	case *message.RequestOK:
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindRequestOK))
		if c.Has(CapTrackPropertiesInRequestOK) {
			b = append(b, v.TrackProperties...)
		}
	case *message.RequestError:
		b = c.putVarint(b, uint64(v.Code))
		b = c.putVarint(b, v.RetryInterval)
		b = c.putString(b, v.Reason)
		if v.Code == message.RequestRedirect && c.Has(CapRedirect) {
			rd := v.Redirect
			if rd == nil {
				rd = &message.Redirect{}
			}
			b = c.putString(b, rd.ConnectURI)
			b = c.putNamespace(b, rd.Track.Namespace)
			b = c.putBytes(b, rd.Track.Name)
		}
	case *message.RequestUpdate:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindRequestUpdate))
	case *message.Subscribe:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putTrack(b, v.Track)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindSubscribe))
	case *message.SubscribeOK:
		b = c.putVarint(b, v.TrackAlias)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindSubscribeOK))
		b = append(b, v.TrackProperties...)
	case *message.Publish:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putTrack(b, v.Track)
		b = c.putVarint(b, v.TrackAlias)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindPublish))
		b = append(b, v.TrackProperties...)
	case *message.PublishOK:
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindPublishOK))
	case *message.PublishDone:
		b = c.putVarint(b, uint64(c.doneCode(v.Code)))
		b = c.putVarint(b, v.StreamCount)
		b = c.putString(b, v.Reason)
	case *message.PublishStateNotify:
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindPublishStateNotify))
	case *message.Fetch:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		if c.Has(CapJoiningFetch) {
			b, err = c.appendLegacyFetch(b, v)
			break
		}
		b = c.putTrack(b, v.Track)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindFetch))
	case *message.FetchOK:
		b = append(b, boolByte(v.EndOfTrack))
		end := v.EndLocation
		if c.Has(CapJoiningFetch) {
			end = legacyFetchEnd(end)
		}
		b = c.putLocation(b, end)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindFetchOK))
		b = append(b, v.TrackProperties...)
	case *message.TrackStatus:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putTrack(b, v.Track)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindTrackStatus))
	case *message.PublishNamespace:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putNamespace(b, v.Prefix)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindPublishNamespace))
	case *message.SubscribeNamespace:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putNamespace(b, v.Prefix)
		if !c.Has(CapSubscribeTracks) {
			b = c.putVarint(b, v.Options)
		}
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindSubscribeNamespace))
	case *message.Namespace:
		b = c.putNamespace(b, v.Suffix)
	case *message.NamespaceDone:
		b = c.putNamespace(b, v.Suffix)
	case *message.SubscribeTracks:
		b = c.putVarint(b, v.RequestID)
		b = c.putNamespace(b, v.Prefix)
		b, err = c.appendParams(b, &v.Params, message.AllowedParams(message.KindSubscribeTracks))
	case *message.PublishSkipped:
		b = c.putNamespace(b, v.Suffix)
		b = c.putBytes(b, v.TrackName)
	default:
		err = fmt.Errorf("%w: %T", ErrUnknownMessage, m)
	}
	if err != nil {
		return b[:start], err
	}
	b, err = wire.EndMessage(b, at)
	if err != nil {
		return b[:start], err
	}
	return b, nil
}

func checkNames(m message.Message) error {
	// a name the peer would reject as malformed must not leave this endpoint, since the peer closes the session
	switch v := m.(type) {
	case *message.Subscribe:
		return v.Track.Namespace.Validate(v.Track.Name)
	case *message.Publish:
		return v.Track.Namespace.Validate(v.Track.Name)
	case *message.Fetch:
		return v.Track.Namespace.Validate(v.Track.Name)
	case *message.TrackStatus:
		return v.Track.Namespace.Validate(v.Track.Name)
	case *message.PublishNamespace:
		return v.Prefix.Validate(nil)
	case *message.SubscribeNamespace:
		return v.Prefix.Validate(nil)
	case *message.SubscribeTracks:
		return v.Prefix.Validate(nil)
	case *message.Namespace:
		return v.Suffix.Validate(nil)
	case *message.NamespaceDone:
		return v.Suffix.Validate(nil)
	case *message.PublishSkipped:
		return v.Suffix.Validate(v.TrackName)
	case *message.RequestError:
		if v.Redirect != nil {
			return v.Redirect.Track.Namespace.Validate(v.Redirect.Track.Name)
		}
	}
	return nil
}

func (c *Codec) putRequestID(b []byte, id, required uint64) []byte {
	b = c.putVarint(b, id)
	if c.Has(CapRequiredRequestID) {
		b = c.putVarint(b, required)
	}
	return b
}

func (c *Codec) putTrack(b []byte, t message.FullTrackName) []byte {
	return c.putBytes(c.putNamespace(b, t.Namespace), t.Name)
}

func (c *Codec) appendSetup(b []byte, s *message.Setup) ([]byte, error) {
	kvs := make([]wire.KV, 0, 8+len(s.AuthTokens))
	if s.HasPath {
		kvs = append(kvs, wire.KV{Type: OptionPath, Bytes: []byte(s.Path)})
	}
	for _, tok := range s.AuthTokens {
		tb, err := wire.AppendToken(nil, tok)
		if err != nil {
			return b, err
		}
		kvs = append(kvs, wire.KV{Type: OptionAuthorizationToken, Bytes: tb})
	}
	if s.MaxAuthTokenCacheSize > 0 {
		kvs = append(kvs, wire.KV{Type: OptionMaxAuthTokenCacheSize, Uint: s.MaxAuthTokenCacheSize})
	}
	if s.HasAuthority {
		kvs = append(kvs, wire.KV{Type: OptionAuthority, Bytes: []byte(s.Authority)})
	}
	if s.Implementation != "" {
		kvs = append(kvs, wire.KV{Type: OptionImplementation, Bytes: []byte(s.Implementation)})
	}
	if c.Has(CapRangeFilters) {
		if s.MaxFilterRanges > 0 {
			kvs = append(kvs, wire.KV{Type: OptionMaxFilterRanges, Uint: s.MaxFilterRanges})
		}
		if s.MaxRequestUpdates > 0 {
			kvs = append(kvs, wire.KV{Type: OptionMaxRequestUpdates, Uint: s.MaxRequestUpdates})
		}
	}
	kvs = append(kvs, s.Unknown...)
	wire.SortKVs(kvs)
	return wire.AppendKVs(b, kvs)
}

// ParseMessage decodes the body of a control message of wire type typ. A malformed message returns a
// *message.SessionError naming the code that ends the session.
func (c *Codec) ParseMessage(typ uint64, body []byte) (message.Message, error) {
	k, ok := c.MessageKind(typ)
	if !ok {
		return nil, message.Violation(fmt.Sprintf("unknown message type %#x", typ))
	}
	r := wire.NewReader(body)
	var m message.Message
	var err error
	switch k {
	case message.KindSetup:
		m, err = c.parseSetup(body)
		return m, err
	case message.KindGoAway:
		v := &message.GoAway{NewSessionURI: r.URI(), Timeout: r.Varint()}
		if c.Has(CapGoAwayRequestID) && r.Remaining() > 0 {
			v.RequestID, v.HasRequestID = r.Varint(), true
		}
		m = v
	case message.KindRequestOK:
		v := &message.RequestOK{}
		if v.Params, err = c.parseParams(&r, message.AllowedParams(k)); err == nil &&
			c.Has(CapTrackPropertiesInRequestOK) {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindRequestError:
		v := &message.RequestError{
			Code: message.RequestCode(r.Varint()), RetryInterval: r.Varint(), Reason: r.Reason(),
		}
		if v.Code == message.RequestRedirect && c.Has(CapRedirect) && r.Err() == nil {
			rd := &message.Redirect{ConnectURI: r.URI()}
			rd.Track.Namespace = r.Namespace()
			rd.Track.Name = r.TrackName(rd.Track.Namespace)
			v.Redirect = rd
		}
		m = v
	case message.KindRequestUpdate:
		v := &message.RequestUpdate{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindSubscribe:
		v := &message.Subscribe{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Track = readTrack(&r)
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindSubscribeOK:
		v := &message.SubscribeOK{TrackAlias: r.Varint()}
		if v.Params, err = c.parseParams(&r, message.AllowedParams(k)); err == nil {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindPublish:
		v := &message.Publish{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Track = readTrack(&r)
		v.TrackAlias = r.Varint()
		if v.Params, err = c.parseParams(&r, message.AllowedParams(k)); err == nil {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindPublishOK:
		v := &message.PublishOK{}
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindPublishDone:
		m = &message.PublishDone{
			Code: c.doneCode(message.DoneCode(r.Varint())), StreamCount: r.Varint(),
			Reason: r.Reason(),
		}
	case message.KindPublishStateNotify:
		v := &message.PublishStateNotify{}
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindFetch:
		v := &message.Fetch{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		if c.Has(CapJoiningFetch) {
			err = c.parseLegacyFetch(&r, v)
		} else {
			v.Track = readTrack(&r)
			v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		}
		m = v
	case message.KindFetchOK:
		v := &message.FetchOK{}
		eot := r.Uint8()
		if eot > 1 {
			return nil, message.Violation("end of track flag out of range")
		}
		v.EndOfTrack = eot == 1
		v.EndLocation = r.Location()
		if c.Has(CapJoiningFetch) {
			v.EndLocation = inclusiveFetchEnd(v.EndLocation)
		}
		if v.Params, err = c.parseParams(&r, message.AllowedParams(k)); err == nil {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindTrackStatus:
		v := &message.TrackStatus{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Track = readTrack(&r)
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindPublishNamespace:
		v := &message.PublishNamespace{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Prefix = r.Namespace()
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindSubscribeNamespace:
		v := &message.SubscribeNamespace{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Prefix = r.Namespace()
		if !c.Has(CapSubscribeTracks) {
			if v.Options = r.Varint(); v.Options > message.SubscribeOptionBoth && r.Err() == nil {
				return nil, message.Violation("unknown subscribe options")
			}
		}
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindNamespace:
		m = &message.Namespace{Suffix: r.Namespace()}
	case message.KindNamespaceDone:
		m = &message.NamespaceDone{Suffix: r.Namespace()}
	case message.KindSubscribeTracks:
		v := &message.SubscribeTracks{RequestID: r.Varint(), Prefix: r.Namespace()}
		v.Params, err = c.parseParams(&r, message.AllowedParams(k))
		m = v
	case message.KindPublishSkipped:
		v := &message.PublishSkipped{Suffix: r.Namespace()}
		v.TrackName = r.TrackName(v.Suffix)
		m = v
	}
	if err != nil {
		return nil, err
	}
	if err = r.Done(); err != nil {
		return nil, wireViolation(err)
	}
	return m, nil
}

func (c *Codec) doneCode(code message.DoneCode) message.DoneCode {
	// draft -17 swaps the codes of EXPIRED and TOO_FAR_BEHIND; the swap is its own inverse
	if c.Has(CapLegacyDoneCodes) {
		switch code {
		case message.DoneTooFarBehind:
			return message.DoneExpired
		case message.DoneExpired:
			return message.DoneTooFarBehind
		}
	}
	return code
}

func (c *Codec) appendLegacyFetch(b []byte, f *message.Fetch) ([]byte, error) {
	// the range is a Standalone Fetch in the message, its End Location one past the last object or {group, 0}
	// for all of a group; the joining forms name a subscription instead (through -19)
	p := f.Params
	p.Set &^= message.Bit(message.ParamLocationFilter)
	if j := f.Joining; j != nil {
		kind := uint64(fetchTypeAbsoluteJoining)
		if j.Relative {
			kind = fetchTypeRelativeJoining
		}
		b = c.putVarint(c.putVarint(c.putVarint(b, kind), j.RequestID), j.Start)
		return c.appendParams(b, &p, message.AllowedParams(message.KindFetch))
	}
	lf := f.Params.LocationFilter
	start := wire.Location{Group: lf.StartGroup, Object: lf.StartObject}
	var end wire.Location
	switch lf.Type {
	case message.FilterAbsoluteRange:
		if lf.EndObject == math.MaxUint64 {
			return b, ErrLocationFilter
		}
		end = wire.Location{Group: lf.StartGroup + lf.EndGroupDelta, Object: lf.EndObject + 1}
	case message.FilterAbsoluteStartGroupEnd:
		end = wire.Location{Group: lf.StartGroup + lf.EndGroupDelta}
	default:
		return b, ErrLocationFilter
	}
	b = c.putTrack(c.putVarint(b, fetchTypeStandalone), f.Track)
	b = c.putLocation(c.putLocation(b, start), end)
	return c.appendParams(b, &p, message.AllowedParams(message.KindFetch))
}

func legacyFetchEnd(end wire.Location) wire.Location {
	// through -19 FETCH_OK's End Location is coded like FETCH's: one past the last object, or {group, 0} for all of it
	if end.Object == math.MaxUint64 {
		return wire.Location{Group: end.Group}
	}
	return wire.Location{Group: end.Group, Object: end.Object + 1}
}

func inclusiveFetchEnd(end wire.Location) wire.Location {
	if end.Object == 0 {
		return wire.Location{Group: end.Group, Object: math.MaxUint64}
	}
	return wire.Location{Group: end.Group, Object: end.Object - 1}
}

func (c *Codec) parseLegacyFetch(r *wire.Reader, f *message.Fetch) error {
	switch kind := r.Varint(); kind {
	case fetchTypeStandalone:
		f.Track = readTrack(r)
		start, end := r.Location(), r.Location()
		var err error
		if f.Params, err = c.parseParams(r, message.AllowedParams(message.KindFetch)); err != nil {
			return err
		}
		if r.Err() != nil {
			return nil
		}
		if end.Group < start.Group {
			return message.Violation("fetch ends before it starts")
		}
		lf := message.LocationFilter{
			Type: message.FilterAbsoluteStartGroupEnd, StartGroup: start.Group, StartObject: start.Object,
			EndGroupDelta: end.Group - start.Group,
		}
		if end.Object > 0 {
			lf.Type, lf.EndObject = message.FilterAbsoluteRange, end.Object-1
		}
		f.Params.SetLocationFilter(lf)
		return nil
	case fetchTypeRelativeJoining, fetchTypeAbsoluteJoining:
		f.Joining = &message.JoiningFetch{RequestID: r.Varint(), Start: r.Varint(), Relative: kind == fetchTypeRelativeJoining}
		var err error
		f.Params, err = c.parseParams(r, message.AllowedParams(message.KindFetch))
		return err
	default:
		if r.Err() != nil {
			return nil
		}
		return message.Violation("unknown fetch type")
	}
}

func (c *Codec) readRequestID(r *wire.Reader) (id, required uint64) {
	id = r.Varint()
	if c.Has(CapRequiredRequestID) {
		required = r.Varint()
	}
	return id, required
}

func readTrack(r *wire.Reader) message.FullTrackName {
	var t message.FullTrackName
	t.Namespace = r.Namespace()
	t.Name = r.TrackName(t.Namespace)
	return t
}

func (c *Codec) parseSetup(body []byte) (*message.Setup, error) {
	s := &message.Setup{}
	var seen message.ParamSet
	once := func(t uint64) bool {
		// a repeated known option is ignored after its first instance
		bit := message.ParamSet(1) << t
		if seen&bit != 0 {
			return false
		}
		seen |= bit
		return true
	}
	it := wire.NewKVIter(body)
	for kv, ok := it.Next(); ok; kv, ok = it.Next() {
		switch kv.Type {
		case OptionPath:
			if once(kv.Type) {
				s.Path, s.HasPath = string(kv.Bytes), true
			}
		case OptionAuthorizationToken:
			tok, err := wire.ParseToken(kv.Bytes)
			if err != nil {
				return nil, &message.SessionError{Code: message.CodeKeyValueFormatting, Reason: err.Error()}
			}
			s.AuthTokens = append(s.AuthTokens, tok)
		case OptionMaxAuthTokenCacheSize:
			if once(kv.Type) {
				s.MaxAuthTokenCacheSize = kv.Uint
			}
		case OptionAuthority:
			if once(kv.Type) {
				s.Authority, s.HasAuthority = string(kv.Bytes), true
			}
		case OptionImplementation:
			if once(kv.Type) {
				s.Implementation = string(kv.Bytes)
			}
		case OptionMaxFilterRanges, OptionMaxRequestUpdates:
			if !c.Has(CapRangeFilters) {
				s.Unknown = append(s.Unknown, kv)
				continue
			}
			if once(kv.Type) {
				if kv.Type == OptionMaxFilterRanges {
					s.MaxFilterRanges = kv.Uint
				} else {
					s.MaxRequestUpdates = kv.Uint
				}
			}
		default:
			s.Unknown = append(s.Unknown, kv)
		}
	}
	if err := it.Err(); err != nil {
		return nil, wireViolation(err)
	}
	return s, nil
}
