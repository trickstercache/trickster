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
	"io"
	"math"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// Unidirectional stream and datagram types
const (
	StreamTypeFetch     uint64 = 0x05
	StreamTypeControl   uint64 = 0x2f00
	StreamTypePadding   uint64 = 0x132b3e28
	DatagramTypePadding uint64 = 0x132b3e29
)

// MaxObjectPropertyLen bounds an object's Properties block, which the drafts leave unbounded
const MaxObjectPropertyLen = 1 << 20

const (
	subgroupBitProperties     = 0x01
	subgroupMaskIDMode        = 0x06
	subgroupBitEndOfGroup     = 0x08
	subgroupBitAlways         = 0x10
	subgroupBitDefaultPrio    = 0x20
	subgroupBitFirstObject    = 0x40
	datagramBitProperties     = 0x01
	datagramBitEndOfGroup     = 0x02
	datagramBitZeroObject     = 0x04
	datagramBitDefaultPrio    = 0x08
	datagramBitReserved       = 0x10
	datagramBitStatus         = 0x20
	fetchMaskSubgroup         = 0x03
	fetchBitObjectID          = 0x04
	fetchBitGroupID           = 0x08
	fetchBitPriority          = 0x10
	fetchBitProperties        = 0x20
	fetchBitDatagram          = 0x40
	fetchFlagLimit            = 0x80
	fetchEndNonExistent       = 0x8c
	fetchEndUnknown           = 0x10c
	fetchEndTimedOut          = 0x20c
	fetchSubgroupZero         = 0
	fetchSubgroupPrior        = 1
	fetchSubgroupPriorPlusOne = 2
	fetchSubgroupExplicit     = 3
)

// StreamKind classifies a unidirectional stream by its type
type StreamKind uint8

// Unidirectional stream kinds
const (
	StreamUnknown StreamKind = iota
	StreamControl
	StreamSubgroup
	StreamFetch
	StreamPadding
)

var (
	// ErrObjectOrder is returned when encoding objects out of the order their stream requires
	ErrObjectOrder = errors.New("moq: objects out of order for their stream")
	// ErrObjectFields is returned when encoding an object whose fields cannot appear together
	ErrObjectFields = errors.New("moq: object fields cannot be encoded together")
)

// ClassifyStream returns the kind of a unidirectional stream whose type varint is t
func (c *Codec) ClassifyStream(t uint64) StreamKind {
	switch {
	case t == StreamTypeControl:
		return StreamControl
	case t == StreamTypeFetch:
		return StreamFetch
	case t == StreamTypePadding && c.Has(CapPadding):
		return StreamPadding
	case c.validSubgroupType(t):
		return StreamSubgroup
	}
	return StreamUnknown
}

func (c *Codec) validSubgroupType(t uint64) bool {
	if t >= fetchFlagLimit || t&subgroupBitAlways == 0 || t&subgroupMaskIDMode == subgroupMaskIDMode {
		return false
	}
	return t&subgroupBitFirstObject == 0 || c.Has(CapFirstObjectBit)
}

// AppendSubgroupHeader appends a subgroup stream's type and header
func (c *Codec) AppendSubgroupHeader(b []byte, h *message.SubgroupHeader) []byte {
	flags := byte(subgroupBitAlways) | byte(h.Mode)<<1
	if h.HasProperties {
		flags |= subgroupBitProperties
	}
	if h.EndOfGroup {
		flags |= subgroupBitEndOfGroup
	}
	if h.DefaultPriority {
		flags |= subgroupBitDefaultPrio
	}
	if h.FirstObject && c.Has(CapFirstObjectBit) {
		flags |= subgroupBitFirstObject
	}
	b = append(b, flags)
	b = c.putVarint(b, h.TrackAlias)
	b = c.putVarint(b, h.Group)
	if h.Mode == message.SubgroupIDExplicit {
		b = c.putVarint(b, h.Subgroup)
	}
	if !h.DefaultPriority {
		b = append(b, h.Priority)
	}
	return b
}

// SubgroupReader reads the objects of one subgroup stream
type SubgroupReader struct {
	r       wire.StreamReader
	header  message.SubgroupHeader
	prev    uint64
	started bool
}

// ReadSubgroupHeader reads the rest of a subgroup header whose type t was already read, returning a reader for
// the stream's objects
func (c *Codec) ReadSubgroupHeader(t uint64, r wire.StreamReader) (*SubgroupReader, error) {
	if !c.validSubgroupType(t) {
		return nil, message.Violation("invalid subgroup header type")
	}
	h := message.SubgroupHeader{
		Mode:            message.SubgroupIDMode((t & subgroupMaskIDMode) >> 1),
		HasProperties:   t&subgroupBitProperties != 0,
		EndOfGroup:      t&subgroupBitEndOfGroup != 0,
		DefaultPriority: t&subgroupBitDefaultPrio != 0,
		FirstObject:     t&subgroupBitFirstObject != 0,
	}
	var err error
	if h.TrackAlias, err = readStreamVarint(r); err != nil {
		return nil, err
	}
	if h.Group, err = readStreamVarint(r); err != nil {
		return nil, err
	}
	if h.Mode == message.SubgroupIDExplicit {
		if h.Subgroup, err = readStreamVarint(r); err != nil {
			return nil, err
		}
	}
	if !h.DefaultPriority {
		if h.Priority, err = r.ReadByte(); err != nil {
			return nil, unexpectedEOF(err)
		}
	}
	return &SubgroupReader{r: r, header: h}, nil
}

// Header returns the stream's header; with SubgroupIDFirstObject its Subgroup is set once the first object is read
func (s *SubgroupReader) Header() *message.SubgroupHeader {
	return &s.header
}

// Next reads the next object's header, after which the caller reads PayloadLen bytes of payload from the stream.
// io.EOF means the stream ended cleanly between objects; an end within an object is io.ErrUnexpectedEOF.
func (s *SubgroupReader) Next() (message.SubgroupObject, error) {
	var o message.SubgroupObject
	d, err := wire.ReadVarintFrom(s.r)
	if err != nil {
		return o, err
	}
	if !s.started {
		o.ID = d
		s.started = true
		if s.header.Mode == message.SubgroupIDFirstObject {
			s.header.Subgroup = d
		}
	} else {
		if d == math.MaxUint64 || s.prev > math.MaxUint64-d-1 {
			return o, message.Violation("object ID overflows")
		}
		o.ID = s.prev + d + 1
	}
	s.prev = o.ID
	if s.header.HasProperties {
		if o.Properties, err = readProperties(s.r); err != nil {
			return o, err
		}
	}
	if o.PayloadLen, err = readStreamVarint(s.r); err != nil {
		return o, err
	}
	if o.PayloadLen == 0 {
		st, err := readStreamVarint(s.r)
		if err != nil {
			return o, err
		}
		o.Status = message.ObjectStatus(st)
		if !o.Status.Valid() {
			return o, message.Violation("unknown object status")
		}
		if o.Status != message.StatusNormal && len(o.Properties) > 0 {
			return o, message.Violation("properties on an object that is not normal")
		}
	}
	return o, nil
}

func readStreamVarint(r io.ByteReader) (uint64, error) {
	v, err := wire.ReadVarintFrom(r)
	return v, unexpectedEOF(err)
}

func readProperties(r wire.StreamReader) (message.Properties, error) {
	n, err := readStreamVarint(r)
	if err != nil || n == 0 {
		return nil, err
	}
	if n > MaxObjectPropertyLen {
		return nil, message.Violation("object properties too long")
	}
	p := make(message.Properties, n)
	if _, err = io.ReadFull(r, p); err != nil {
		return nil, unexpectedEOF(err)
	}
	return p, nil
}

func unexpectedEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// SubgroupWriter encodes the objects of one subgroup stream after its header
type SubgroupWriter struct {
	c       *Codec
	header  message.SubgroupHeader
	prev    uint64
	started bool
}

// NewSubgroupWriter returns a writer for objects that follow the header h
func (c *Codec) NewSubgroupWriter(h message.SubgroupHeader) *SubgroupWriter {
	return &SubgroupWriter{c: c, header: h}
}

// AppendObject appends an object's header fields; the caller appends PayloadLen bytes of payload after them
func (w *SubgroupWriter) AppendObject(b []byte, o *message.SubgroupObject) ([]byte, error) {
	if len(o.Properties) > 0 && (!w.header.HasProperties || o.Status != message.StatusNormal) {
		return b, ErrObjectFields
	}
	if o.PayloadLen > 0 && o.Status != message.StatusNormal {
		return b, ErrObjectFields
	}
	var d uint64
	switch {
	case !w.started:
		if w.header.Mode == message.SubgroupIDFirstObject && o.ID != w.header.Subgroup {
			return b, ErrObjectFields
		}
		d = o.ID
	case o.ID <= w.prev:
		return b, ErrObjectOrder
	default:
		d = o.ID - w.prev - 1
	}
	w.started, w.prev = true, o.ID
	b = w.c.putVarint(b, d)
	if w.header.HasProperties {
		b = w.c.putBytes(b, o.Properties)
	}
	b = w.c.putVarint(b, o.PayloadLen)
	if o.PayloadLen == 0 {
		b = w.c.putVarint(b, uint64(o.Status))
	}
	return b, nil
}

// IsPaddingDatagram reports whether a datagram is padding, to be discarded
func (c *Codec) IsPaddingDatagram(b []byte) bool {
	t, n := wire.ReadVarint(b)
	return n > 0 && t == DatagramTypePadding && c.Has(CapPadding)
}

// AppendDatagram appends an OBJECT_DATAGRAM
func (c *Codec) AppendDatagram(b []byte, d *message.Datagram) ([]byte, error) {
	if d.HasStatus && (d.EndOfGroup || len(d.Payload) > 0) {
		return b, ErrObjectFields
	}
	if len(d.Properties) > 0 && d.HasStatus && d.Status != message.StatusNormal {
		return b, ErrObjectFields
	}
	var flags byte
	if len(d.Properties) > 0 {
		flags |= datagramBitProperties
	}
	if d.EndOfGroup {
		flags |= datagramBitEndOfGroup
	}
	if d.Object == 0 {
		flags |= datagramBitZeroObject
	}
	if d.DefaultPriority {
		flags |= datagramBitDefaultPrio
	}
	if d.HasStatus {
		flags |= datagramBitStatus
	}
	b = append(b, flags)
	b = c.putVarint(b, d.TrackAlias)
	b = c.putVarint(b, d.Group)
	if d.Object != 0 {
		b = c.putVarint(b, d.Object)
	}
	if !d.DefaultPriority {
		b = append(b, d.Priority)
	}
	if len(d.Properties) > 0 {
		b = c.putBytes(b, d.Properties)
	}
	if d.HasStatus {
		return c.putVarint(b, uint64(d.Status)), nil
	}
	return append(b, d.Payload...), nil
}

// ParseDatagram decodes an OBJECT_DATAGRAM; its payload aliases b
func (c *Codec) ParseDatagram(b []byte) (message.Datagram, error) {
	var d message.Datagram
	r := wire.NewReader(b)
	t := r.Varint()
	if r.Err() != nil {
		return d, wireViolation(r.Err())
	}
	if t >= fetchFlagLimit || t&datagramBitReserved != 0 || t&^0x3f != 0 ||
		(t&datagramBitStatus != 0 && t&datagramBitEndOfGroup != 0) {
		return d, message.Violation("invalid datagram type")
	}
	d.EndOfGroup = t&datagramBitEndOfGroup != 0
	d.DefaultPriority = t&datagramBitDefaultPrio != 0
	d.HasStatus = t&datagramBitStatus != 0
	d.TrackAlias = r.Varint()
	d.Group = r.Varint()
	if t&datagramBitZeroObject == 0 {
		d.Object = r.Varint()
	}
	if !d.DefaultPriority {
		d.Priority = r.Uint8()
	}
	if t&datagramBitProperties != 0 {
		d.Properties = message.Properties(r.LenBytes(MaxObjectPropertyLen))
		if r.Err() == nil && len(d.Properties) == 0 {
			return d, message.Violation("datagram declares empty properties")
		}
	}
	if d.HasStatus {
		d.Status = message.ObjectStatus(r.Varint())
		if err := r.Done(); err != nil {
			return d, wireViolation(err)
		}
		if !d.Status.Valid() {
			return d, message.Violation("unknown object status")
		}
		if d.Status != message.StatusNormal && len(d.Properties) > 0 {
			return d, message.Violation("properties on an object that is not normal")
		}
		return d, nil
	}
	d.Payload = r.Rest()
	if err := r.Err(); err != nil {
		return d, wireViolation(err)
	}
	return d, nil
}

// AppendFetchHeader appends a fetch stream's type and the Request ID it answers
func (c *Codec) AppendFetchHeader(b []byte, requestID uint64) []byte {
	return c.putVarint(append(b, byte(StreamTypeFetch)), requestID)
}

// ReadFetchHeader reads the Request ID of a fetch stream whose type was already read
func (c *Codec) ReadFetchHeader(r wire.StreamReader) (uint64, error) {
	return readStreamVarint(r)
}

type fetchState struct {
	order       message.GroupOrder
	started     bool
	group       uint64
	object      uint64
	hasObject   bool
	hasSubgroup bool
	subgroup    uint64
	priority    uint8
}

func (s *fetchState) nextGroup(gd uint64) (uint64, bool) {
	if s.order == message.GroupOrderDescending {
		if gd >= s.group {
			return 0, false
		}
		return s.group - gd - 1, true
	}
	if gd == math.MaxUint64 || s.group > math.MaxUint64-gd-1 {
		return 0, false
	}
	return s.group + gd + 1, true
}

// FetchReader reads the entries of one fetch stream after its header
type FetchReader struct {
	c *Codec
	r wire.StreamReader
	s fetchState
}

// NewFetchReader returns a reader for a fetch stream delivered in the given group order
func (c *Codec) NewFetchReader(r wire.StreamReader, order message.GroupOrder) *FetchReader {
	if !order.Valid() {
		order = message.GroupOrderAscending
	}
	return &FetchReader{c: c, r: r, s: fetchState{order: order}}
}

// Next reads the next entry; for an object the caller then reads PayloadLen bytes of payload. io.EOF means the
// stream ended cleanly between entries.
func (f *FetchReader) Next() (message.FetchObject, error) {
	var o message.FetchObject
	flags, err := wire.ReadVarintFrom(f.r)
	if err != nil {
		return o, err
	}
	s := &f.s
	if flags >= fetchFlagLimit {
		switch {
		case flags == fetchEndNonExistent:
			o.EndOfRange = message.RangeEndNonExistent
		case flags == fetchEndUnknown:
			o.EndOfRange = message.RangeEndUnknown
		case flags == fetchEndTimedOut && f.c.Has(CapEndOfTimedOutRange):
			o.EndOfRange = message.RangeEndTimedOut
		default:
			return o, message.Violation("invalid fetch serialization flags")
		}
		// a marker names the absolute Location that ends its span
		if o.Group, err = readStreamVarint(f.r); err != nil {
			return o, err
		}
		if o.Object, err = readStreamVarint(f.r); err != nil {
			return o, err
		}
		s.started, s.group, s.object = true, o.Group, o.Object
		return o, nil
	}
	subMode := flags & fetchMaskSubgroup
	o.Datagram = flags&fetchBitDatagram != 0
	if !s.started && (flags&fetchBitGroupID == 0 || flags&fetchBitObjectID == 0) {
		return o, message.Violation("first fetch object refers to a prior object")
	}
	hasGroup := flags&fetchBitGroupID != 0
	o.Group = s.group
	if hasGroup {
		gd, err := readStreamVarint(f.r)
		if err != nil {
			return o, err
		}
		o.Group = gd
		if s.started {
			var ok bool
			if o.Group, ok = s.nextGroup(gd); !ok {
				return o, message.Violation("fetch group ID out of range")
			}
		}
	}
	if !o.Datagram {
		switch subMode {
		case fetchSubgroupZero:
		case fetchSubgroupPrior, fetchSubgroupPriorPlusOne:
			if !s.hasSubgroup {
				return o, message.Violation("fetch object refers to a missing prior subgroup")
			}
			o.Subgroup = s.subgroup
			if subMode == fetchSubgroupPriorPlusOne {
				if o.Subgroup == math.MaxUint64 {
					return o, message.Violation("fetch subgroup ID overflows")
				}
				o.Subgroup++
			}
		case fetchSubgroupExplicit:
			if o.Subgroup, err = readStreamVarint(f.r); err != nil {
				return o, err
			}
		}
	}
	if flags&fetchBitObjectID != 0 {
		od, err := readStreamVarint(f.r)
		if err != nil {
			return o, err
		}
		switch {
		case hasGroup || !s.started:
			o.Object = od
		case od > math.MaxUint64-s.object:
			return o, message.Violation("fetch object ID overflows")
		default:
			o.Object = s.object + od
		}
	} else {
		if s.object == math.MaxUint64 {
			return o, message.Violation("fetch object ID overflows")
		}
		o.Object = s.object + 1
	}
	if flags&fetchBitPriority != 0 {
		if o.Priority, err = f.r.ReadByte(); err != nil {
			return o, unexpectedEOF(err)
		}
	} else {
		if !s.hasObject {
			return o, message.Violation("fetch object refers to a missing prior priority")
		}
		o.Priority = s.priority
	}
	if flags&fetchBitProperties != 0 {
		if o.Properties, err = readProperties(f.r); err != nil {
			return o, err
		}
	}
	if o.PayloadLen, err = readStreamVarint(f.r); err != nil {
		return o, err
	}
	s.started, s.group, s.object, s.hasObject, s.priority = true, o.Group, o.Object, true, o.Priority
	if !o.Datagram {
		s.hasSubgroup, s.subgroup = true, o.Subgroup
	}
	return o, nil
}

// FetchWriter encodes the entries of one fetch stream, choosing the most compact flags
type FetchWriter struct {
	c *Codec
	s fetchState
}

// NewFetchWriter returns a writer for a fetch stream delivered in the given group order
func (c *Codec) NewFetchWriter(order message.GroupOrder) *FetchWriter {
	if !order.Valid() {
		order = message.GroupOrderAscending
	}
	return &FetchWriter{c: c, s: fetchState{order: order}}
}

// AppendObject appends an entry; for an object the caller appends PayloadLen bytes of payload after it
func (w *FetchWriter) AppendObject(b []byte, o *message.FetchObject) ([]byte, error) {
	c, s := w.c, &w.s
	if o.EndOfRange != message.RangeEndNone {
		var flags uint64
		switch o.EndOfRange {
		case message.RangeEndNonExistent:
			flags = fetchEndNonExistent
		case message.RangeEndUnknown:
			flags = fetchEndUnknown
		case message.RangeEndTimedOut:
			if !c.Has(CapEndOfTimedOutRange) {
				return b, ErrObjectFields
			}
			flags = fetchEndTimedOut
		default:
			return b, ErrObjectFields
		}
		b = c.putVarint(b, flags)
		b = c.putLocation(b, wire.Location{Group: o.Group, Object: o.Object})
		s.started, s.group, s.object = true, o.Group, o.Object
		return b, nil
	}
	var flags uint64
	var gd, od uint64
	switch {
	case !s.started:
		flags |= fetchBitGroupID | fetchBitObjectID
		gd, od = o.Group, o.Object
	case o.Group == s.group:
		if o.Object <= s.object {
			return b, ErrObjectOrder
		}
		if o.Object != s.object+1 {
			flags |= fetchBitObjectID
			od = o.Object - s.object
		}
	default:
		ascending := s.order != message.GroupOrderDescending
		if (ascending && o.Group < s.group) || (!ascending && o.Group > s.group) {
			return b, ErrObjectOrder
		}
		flags |= fetchBitGroupID
		if ascending {
			gd = o.Group - s.group - 1
		} else {
			gd = s.group - o.Group - 1
		}
		if s.object == math.MaxUint64 || o.Object != s.object+1 {
			flags |= fetchBitObjectID
			od = o.Object
		}
	}
	subMode := uint64(fetchSubgroupExplicit)
	switch {
	case o.Datagram:
		flags |= fetchBitDatagram
		subMode = fetchSubgroupZero
	case o.Subgroup == 0:
		subMode = fetchSubgroupZero
	case s.hasSubgroup && o.Subgroup == s.subgroup:
		subMode = fetchSubgroupPrior
	case s.hasSubgroup && s.subgroup != math.MaxUint64 && o.Subgroup == s.subgroup+1:
		subMode = fetchSubgroupPriorPlusOne
	}
	flags |= subMode
	if !s.hasObject || o.Priority != s.priority {
		flags |= fetchBitPriority
	}
	if len(o.Properties) > 0 {
		flags |= fetchBitProperties
	}
	b = c.putVarint(b, flags)
	if flags&fetchBitGroupID != 0 {
		b = c.putVarint(b, gd)
	}
	if !o.Datagram && subMode == fetchSubgroupExplicit {
		b = c.putVarint(b, o.Subgroup)
	}
	if flags&fetchBitObjectID != 0 {
		b = c.putVarint(b, od)
	}
	if flags&fetchBitPriority != 0 {
		b = append(b, o.Priority)
	}
	if len(o.Properties) > 0 {
		b = c.putBytes(b, o.Properties)
	}
	b = c.putVarint(b, o.PayloadLen)
	s.started, s.group, s.object, s.hasObject, s.priority = true, o.Group, o.Object, true, o.Priority
	if !o.Datagram {
		s.hasSubgroup, s.subgroup = true, o.Subgroup
	}
	return b, nil
}
