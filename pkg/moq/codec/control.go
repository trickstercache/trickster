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
	OptionMaxControlMessageSize uint64 = 0x09
)

// Error Detail types of a draft -23 REQUEST_ERROR
const (
	detailRedirect            uint64 = 0x01
	detailUnsupportedProperty uint64 = 0x02
)

const (
	fetchTypeStandalone      = 0x1 // the Fetch Types of draft -17 to -19 FETCH
	fetchTypeRelativeJoining = 0x2
	fetchTypeAbsoluteJoining = 0x3
)

var (
	// ErrUnknownMessage is returned when encoding a message the draft does not define
	ErrUnknownMessage = errors.New("moq: message not defined in this draft")
	// ErrEmptyTrackName is returned when encoding an empty Track Name in a draft that requires one
	ErrEmptyTrackName = errors.New("moq: empty track name")

	// a FETCH_OK before -23 carries neither of these, which a -23 REQUEST_OK answering a FETCH may
	legacyFetchOKDropped = message.Of(message.ParamExpires, message.ParamLargestObject)
)

// AppendMessage appends m framed as a control message: its type, its length (16 bits through -22, a varint from -23)
// and its body, which is held to the default MAX_CONTROL_MESSAGE_SIZE every peer accepts
func (c *Codec) AppendMessage(b []byte, m message.Message) ([]byte, error) {
	typ, ok := c.MessageType(m.Kind())
	if !ok {
		return b, fmt.Errorf("%w: %s in draft %d", ErrUnknownMessage, m.Kind(), c.draft)
	}
	if err := c.checkNames(m); err != nil {
		return b, err
	}
	start := len(b)
	begin, end := wire.BeginMessage, wire.EndMessage
	if c.Has(CapVarintMessageLength) {
		begin, end = wire.BeginVarintMessage, wire.EndVarintMessage
	}
	b, at := begin(b, typ)
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
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindRequestOK))
		if c.Has(CapTrackPropertiesInRequestOK) {
			b = append(b, v.TrackProperties...)
		}
	case *message.RequestError:
		b = c.putVarint(b, uint64(c.RequestCode(v.Code)))
		b = c.putVarint(b, v.RetryInterval)
		b = c.putString(b, v.Reason)
		switch {
		case c.Has(CapErrorDetails):
			b, err = c.appendErrorDetails(b, v)
		case v.Code == message.RequestRedirect && c.Has(CapRedirect):
			b = c.putRedirect(b, v.Redirect)
		}
	case *message.RequestUpdate:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindRequestUpdate))
	case *message.Subscribe:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putTrack(b, v.Track)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindSubscribe))
	case *message.SubscribeOK:
		b = c.putVarint(b, v.TrackAlias)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindSubscribeOK))
		b = append(b, v.TrackProperties...)
	case *message.Publish:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putTrack(b, v.Track)
		b = c.putVarint(b, v.TrackAlias)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindPublish))
		b = append(b, v.TrackProperties...)
	case *message.PublishOK:
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindPublishOK))
	case *message.PublishDone:
		b = c.putVarint(b, uint64(c.sentDoneCode(v.Code)))
		b = c.putVarint(b, v.StreamCount)
		b = c.putString(b, v.Reason)
	case *message.PublishStateNotify:
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindPublishStateNotify))
	case *message.Fetch:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		if c.Has(CapJoiningFetch) {
			b, err = c.appendLegacyFetch(b, v)
			break
		}
		b = c.putTrack(b, v.Track)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindFetch))
	case *message.FetchOK:
		b, err = c.appendFetchOK(b, v)
	case *message.TrackStatus:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putTrack(b, v.Track)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindTrackStatus))
	case *message.PublishNamespace:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putNamespace(b, v.Prefix)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindPublishNamespace))
	case *message.SubscribeNamespace:
		b = c.putRequestID(b, v.RequestID, v.RequiredRequestID)
		b = c.putNamespace(b, v.Prefix)
		if !c.Has(CapSubscribeTracks) {
			b = c.putVarint(b, v.Options)
		}
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindSubscribeNamespace))
	case *message.Namespace:
		b = c.putNamespace(b, v.Suffix)
		if c.Has(CapNamespaceParams) {
			b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindNamespace))
		}
	case *message.NamespaceDone:
		b = c.putNamespace(b, v.Suffix)
	case *message.SubscribeTracks:
		b = c.putVarint(b, v.RequestID)
		b = c.putNamespace(b, v.Prefix)
		b, err = c.appendParams(b, &v.Params, c.allowedParams(message.KindSubscribeTracks))
	case *message.PublishSkipped:
		b = c.putNamespace(b, v.Suffix)
		b = c.putBytes(b, v.TrackName)
	default:
		err = fmt.Errorf("%w: %T", ErrUnknownMessage, m)
	}
	if err != nil {
		return b[:start], err
	}
	b, err = end(b, at)
	if err != nil {
		return b[:start], err
	}
	return b, nil
}

// ReadMessage reads one control message, its type and its body, which is read into buf when it fits there. io.EOF
// before the type is returned as is; a body over the default MAX_CONTROL_MESSAGE_SIZE is a *message.SessionError.
func (c *Codec) ReadMessage(r wire.StreamReader, buf []byte) (uint64, []byte, error) {
	return c.ReadMessageLimit(r, buf, wire.MaxMessageLen)
}

// ReadMessageLimit reads one control message as ReadMessage does, with limit, the MAX_CONTROL_MESSAGE_SIZE this
// endpoint advertised, bounding the body from -23
func (c *Codec) ReadMessageLimit(r wire.StreamReader, buf []byte, limit uint64) (uint64, []byte, error) {
	typ, err := wire.ReadVarintFrom(r)
	if err != nil {
		return 0, nil, err
	}
	body, err := c.ReadMessageBodyLimit(r, buf, limit)
	return typ, body, err
}

// ReadMessageBody reads a control message's length and body after its type, as ReadMessage does
func (c *Codec) ReadMessageBody(r wire.StreamReader, buf []byte) ([]byte, error) {
	return c.ReadMessageBodyLimit(r, buf, wire.MaxMessageLen)
}

// ReadMessageBodyLimit reads a control message's length and body after its type, as ReadMessageLimit does
func (c *Codec) ReadMessageBodyLimit(r wire.StreamReader, buf []byte, limit uint64) ([]byte, error) {
	if !c.Has(CapVarintMessageLength) {
		return wire.ReadMessageBody(r, buf)
	}
	body, err := wire.ReadVarintMessageBody(r, buf, max(limit, wire.MaxMessageLen))
	if errors.Is(err, wire.ErrMessageTooLong) {
		return nil, c.SessionError(message.CodeFieldLengthExceeded, "control message longer than MAX_CONTROL_MESSAGE_SIZE")
	}
	return body, err
}

func (c *Codec) allowedParams(k message.Kind) message.ParamSet {
	return c.allowed[k]
}

func (c *Codec) draftParams(k message.Kind) message.ParamSet {
	// the parameters a message of kind k may carry in this draft, computed once per codec
	s := message.AllowedParams(k) & c.known
	switch k {
	case message.KindFetchOK:
		if !c.Has(CapFetchRequestOK) {
			return 0
		}
	case message.KindPublish:
		if !c.Has(CapParams23) {
			s &^= message.Bit(message.ParamFillParameters)
		}
	case message.KindSubscribeTracks:
		if c.Has(CapParams23) {
			s &^= message.Of(message.ParamRendezvousTimeout, message.ParamNewGroupRequest)
		} else {
			s &^= message.Bit(message.ParamFillParameters)
		}
	}
	return s
}

func (c *Codec) appendFetchOK(b []byte, v *message.FetchOK) ([]byte, error) {
	// from -23 a REQUEST_OK whose LARGEST_OBJECT says where the response ends when it ends there; earlier drafts
	// state the end and End Of Track, and carry neither EXPIRES nor LARGEST_OBJECT
	p := &v.Params
	if !c.Has(CapFetchRequestOK) {
		b = append(b, boolByte(v.EndOfTrack))
		end := v.EndLocation
		if c.Has(CapJoiningFetch) {
			end = legacyFetchEnd(end)
		}
		b = c.putLocation(b, end)
		if p.Set&legacyFetchOKDropped != 0 {
			legacy := *p
			legacy.Set &^= legacyFetchOKDropped
			p = &legacy
		}
	}
	b, err := c.appendParams(b, p, c.allowedParams(message.KindFetchOK))
	return append(b, v.TrackProperties...), err
}

func (c *Codec) putRedirect(b []byte, rd *message.Redirect) []byte {
	if rd == nil {
		rd = &message.Redirect{}
	}
	b = c.putString(b, rd.ConnectURI)
	b = c.putNamespace(b, rd.Track.Namespace)
	return c.putBytes(b, rd.Track.Name)
}

func (c *Codec) appendErrorDetails(b []byte, v *message.RequestError) ([]byte, error) {
	// each detail goes only with the code it is defined for, REDIRECT's being required
	var kvs []wire.KV
	switch v.Code {
	case message.RequestRedirect:
		kvs = append(kvs, wire.KV{Type: detailRedirect, Bytes: c.putRedirect(nil, v.Redirect)})
	case message.RequestUnsupportedExtension:
		for _, t := range v.UnsupportedProperties {
			kvs = append(kvs, wire.KV{Type: detailUnsupportedProperty, Uint: t})
		}
	}
	return wire.AppendKVs(b, kvs)
}

func (c *Codec) parseErrorDetails(r *wire.Reader, v *message.RequestError) error {
	// unknown details, and known ones with a code they are not defined for, are ignored
	it := wire.NewKVIter(r.Rest())
	for kv, ok := it.Next(); ok; kv, ok = it.Next() {
		switch {
		case kv.Type == detailRedirect && v.Code == message.RequestRedirect && v.Redirect == nil:
			sub := wire.NewReader(kv.Bytes)
			rd := &message.Redirect{ConnectURI: sub.URI()}
			rd.Track.Namespace = sub.Namespace()
			rd.Track.Name = sub.TrackName(rd.Track.Namespace)
			if err := sub.Done(); err != nil {
				return c.framingError(err, message.CodeKeyValueFormatting)
			}
			v.Redirect = rd
		case kv.Type == detailUnsupportedProperty && v.Code == message.RequestUnsupportedExtension:
			v.UnsupportedProperties = append(v.UnsupportedProperties, kv.Uint)
		}
	}
	if err := it.Err(); err != nil {
		return c.wireError(err)
	}
	if v.Code == message.RequestRedirect && v.Redirect == nil {
		return c.SessionError(message.CodeMalformedMessage, "REDIRECT without its detail")
	}
	return nil
}

func (c *Codec) checkNames(m message.Message) error {
	// a name the peer would reject as malformed must not leave this endpoint, since the peer closes the session
	switch v := m.(type) {
	case *message.Subscribe:
		return c.checkTrack(v.Track)
	case *message.Publish:
		return c.checkTrack(v.Track)
	case *message.Fetch:
		return c.checkTrack(v.Track)
	case *message.TrackStatus:
		return c.checkTrack(v.Track)
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
		return c.checkTrack(message.FullTrackName{Namespace: v.Suffix, Name: v.TrackName})
	case *message.RequestError:
		if v.Redirect != nil {
			return v.Redirect.Track.Namespace.Validate(v.Redirect.Track.Name)
		}
	}
	return nil
}

func (c *Codec) checkTrack(t message.FullTrackName) error {
	if len(t.Name) == 0 && c.Has(CapNonEmptyTrackNames) {
		return ErrEmptyTrackName
	}
	return t.Namespace.Validate(t.Name)
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
	if c.Has(CapVarintMessageLength) && s.MaxControlMessageSize > 0 {
		// an odd option type, so the varint is the value of a length-prefixed pair
		v := c.putVarint(nil, max(s.MaxControlMessageSize, wire.MaxMessageLen))
		kvs = append(kvs, wire.KV{Type: OptionMaxControlMessageSize, Bytes: v})
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
		return nil, c.SessionError(message.CodeUnexpectedMessage, fmt.Sprintf("unknown message type %#x", typ))
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
		if v.Params, err = c.parseParams(&r, c.allowedParams(k)); err == nil &&
			c.Has(CapTrackPropertiesInRequestOK) {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindRequestError:
		v := &message.RequestError{
			Code: message.RequestCode(r.Varint()), RetryInterval: r.Varint(), Reason: r.Reason(),
		}
		switch {
		case r.Err() != nil:
		case c.Has(CapErrorDetails):
			err = c.parseErrorDetails(&r, v)
		case v.Code == message.RequestRedirect && c.Has(CapRedirect):
			rd := &message.Redirect{ConnectURI: r.URI()}
			rd.Track.Namespace = r.Namespace()
			rd.Track.Name = r.TrackName(rd.Track.Namespace)
			v.Redirect = rd
		}
		m = v
	case message.KindRequestUpdate:
		v := &message.RequestUpdate{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
		m = v
	case message.KindSubscribe:
		v := &message.Subscribe{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Track = readTrack(&r)
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
		m = v
	case message.KindSubscribeOK:
		v := &message.SubscribeOK{TrackAlias: r.Varint()}
		if v.Params, err = c.parseParams(&r, c.allowedParams(k)); err == nil {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindPublish:
		v := &message.Publish{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Track = readTrack(&r)
		v.TrackAlias = r.Varint()
		if v.Params, err = c.parseParams(&r, c.allowedParams(k)); err == nil {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindPublishOK:
		v := &message.PublishOK{}
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
		m = v
	case message.KindPublishDone:
		m = &message.PublishDone{
			Code: c.doneCode(message.DoneCode(r.Varint())), StreamCount: r.Varint(),
			Reason: r.Reason(),
		}
	case message.KindPublishStateNotify:
		v := &message.PublishStateNotify{}
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
		m = v
	case message.KindFetch:
		v := &message.Fetch{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		if c.Has(CapJoiningFetch) {
			err = c.parseLegacyFetch(&r, v)
		} else {
			v.Track = readTrack(&r)
			v.Params, err = c.parseParams(&r, c.allowedParams(k))
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
		if v.Params, err = c.parseParams(&r, c.allowedParams(k)); err == nil {
			v.TrackProperties = message.Properties(r.Rest())
		}
		m = v
	case message.KindTrackStatus:
		v := &message.TrackStatus{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Track = readTrack(&r)
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
		m = v
	case message.KindPublishNamespace:
		v := &message.PublishNamespace{}
		v.RequestID, v.RequiredRequestID = c.readRequestID(&r)
		v.Prefix = r.Namespace()
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
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
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
		m = v
	case message.KindNamespace:
		v := &message.Namespace{Suffix: r.Namespace()}
		if c.Has(CapNamespaceParams) {
			v.Params, err = c.parseParams(&r, c.allowedParams(k))
		}
		m = v
	case message.KindNamespaceDone:
		m = &message.NamespaceDone{Suffix: r.Namespace()}
	case message.KindSubscribeTracks:
		v := &message.SubscribeTracks{RequestID: r.Varint(), Prefix: r.Namespace()}
		v.Params, err = c.parseParams(&r, c.allowedParams(k))
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
		return nil, c.wireError(err)
	}
	if err = c.checkReceivedNames(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *Codec) checkReceivedNames(m message.Message) error {
	// from -23 a Track Name holds at least one byte
	if !c.Has(CapNonEmptyTrackNames) {
		return nil
	}
	var name []byte
	switch v := m.(type) {
	case *message.Subscribe:
		name = v.Track.Name
	case *message.Publish:
		name = v.Track.Name
	case *message.Fetch:
		name = v.Track.Name
	case *message.TrackStatus:
		name = v.Track.Name
	case *message.PublishSkipped:
		name = v.TrackName
	default:
		return nil
	}
	if len(name) == 0 {
		return c.SessionError(message.CodeProtocolViolation, "empty track name")
	}
	return nil
}

func (c *Codec) sentDoneCode(code message.DoneCode) message.DoneCode {
	// a draft before -23 has no EXPIRED_AUTH_TOKEN, and learns only that the subscription is no longer authorized
	if code == message.DoneExpiredAuthToken && !c.Has(CapSpecificSessionCodes) {
		return message.DoneUnauthorized
	}
	return c.doneCode(code)
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
				return nil, c.tokenError(err)
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
		case OptionMaxControlMessageSize:
			if !c.Has(CapVarintMessageLength) {
				s.Unknown = append(s.Unknown, kv)
				continue
			}
			if once(kv.Type) {
				// the option may not lower the size below the default, which every endpoint accepts
				r := wire.NewReader(kv.Bytes)
				v := r.Varint()
				if err := r.Done(); err != nil {
					return nil, c.SessionError(message.CodeKeyValueFormatting, "malformed MAX_CONTROL_MESSAGE_SIZE")
				}
				s.MaxControlMessageSize = max(v, wire.MaxMessageLen)
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
		return nil, c.wireError(err)
	}
	return s, nil
}

func (c *Codec) tokenError(err error) error {
	// an undecodable token is MALFORMED_AUTH_TOKEN from -23, and a key-value formatting error before it
	if c.Has(CapSpecificSessionCodes) {
		return c.SessionError(message.CodeMalformedAuthToken, err.Error())
	}
	return c.SessionError(message.CodeKeyValueFormatting, err.Error())
}
