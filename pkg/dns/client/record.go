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

package client

import (
	"bytes"
	"encoding/binary"
	"net/netip"
)

const (
	// recordFixedLen is the size of the type, class, TTL and data-length
	// fields that follow a record's name
	recordFixedLen = 10
	// srvFixedLen is the size of the priority, weight and port fields that
	// precede an SRV record's target name
	srvFixedLen = 6
	maxRDataLen = 65535
)

// RecordHeader is the preamble every resource record carries
type RecordHeader struct {
	Name  string
	Type  Type
	Class Class
	TTL   uint32
}

// Record is a DNS resource record. Only the types this client resolves have
// concrete representations; every other type is carried as an *Unknown.
type Record interface {
	// Header returns the record's name, type, class and TTL
	Header() RecordHeader
	packRData(dst []byte) ([]byte, error)
}

// A is an IPv4 address record
type A struct {
	Hdr  RecordHeader
	Addr netip.Addr
}

// Header returns the record's name, type, class and TTL
func (r *A) Header() RecordHeader { return r.Hdr }

func (r *A) packRData(dst []byte) ([]byte, error) {
	if !r.Addr.Is4() {
		return nil, ErrInvalidRecord
	}
	b := r.Addr.As4()
	return append(dst, b[:]...), nil
}

// AAAA is an IPv6 address record
type AAAA struct {
	Hdr  RecordHeader
	Addr netip.Addr
}

// Header returns the record's name, type, class and TTL
func (r *AAAA) Header() RecordHeader { return r.Hdr }

func (r *AAAA) packRData(dst []byte) ([]byte, error) {
	if !r.Addr.IsValid() || r.Addr.Is4() {
		return nil, ErrInvalidRecord
	}
	b := r.Addr.As16()
	return append(dst, b[:]...), nil
}

// SRV is a service location record
type SRV struct {
	Hdr      RecordHeader
	Priority uint16
	Weight   uint16
	Port     uint16
	Target   string
}

// Header returns the record's name, type, class and TTL
func (r *SRV) Header() RecordHeader { return r.Hdr }

func (r *SRV) packRData(dst []byte) ([]byte, error) {
	dst = binary.BigEndian.AppendUint16(dst, r.Priority)
	dst = binary.BigEndian.AppendUint16(dst, r.Weight)
	dst = binary.BigEndian.AppendUint16(dst, r.Port)
	return packName(dst, r.Target)
}

// Unknown carries the raw data of a record whose type this client does not
// interpret, so that unrelated answers do not fail a message
type Unknown struct {
	Hdr  RecordHeader
	Data []byte
}

// Header returns the record's name, type, class and TTL
func (r *Unknown) Header() RecordHeader { return r.Hdr }

func (r *Unknown) packRData(dst []byte) ([]byte, error) {
	return append(dst, r.Data...), nil
}

// packRecord appends rr to dst, backfilling the data-length field once the
// record's data has been written
func packRecord(dst []byte, rr Record) ([]byte, error) {
	h := rr.Header()
	dst, err := packName(dst, h.Name)
	if err != nil {
		return nil, err
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(h.Type))
	dst = binary.BigEndian.AppendUint16(dst, uint16(h.Class))
	dst = binary.BigEndian.AppendUint32(dst, h.TTL)
	lenOff := len(dst)
	dst = binary.BigEndian.AppendUint16(dst, 0)
	if dst, err = rr.packRData(dst); err != nil {
		return nil, err
	}
	n := len(dst) - lenOff - 2
	if n > maxRDataLen {
		return nil, ErrLongMessage
	}
	// #nosec G115 -- n is a section length, range-checked above
	binary.BigEndian.PutUint16(dst[lenOff:lenOff+2], uint16(n))
	return dst, nil
}

// unpackRecord reads the record at off and returns the offset just past it
func unpackRecord(msg []byte, off int) (Record, int, error) {
	name, off, err := unpackName(msg, off)
	if err != nil {
		return nil, 0, err
	}
	if off+recordFixedLen > len(msg) {
		return nil, 0, ErrShortMessage
	}
	h := RecordHeader{
		Name:  name,
		Type:  Type(binary.BigEndian.Uint16(msg[off : off+2])),
		Class: Class(binary.BigEndian.Uint16(msg[off+2 : off+4])),
		TTL:   binary.BigEndian.Uint32(msg[off+4 : off+8]),
	}
	rdLen := int(binary.BigEndian.Uint16(msg[off+8 : off+10]))
	off += recordFixedLen
	end := off + rdLen
	if end > len(msg) {
		return nil, 0, ErrShortMessage
	}
	rr, err := unpackRData(h, msg, off, end)
	if err != nil {
		return nil, 0, err
	}
	return rr, end, nil
}

func unpackRData(h RecordHeader, msg []byte, off, end int) (Record, error) {
	switch h.Type {
	case TypeA:
		addr, ok := netip.AddrFromSlice(msg[off:end])
		if !ok || !addr.Is4() {
			return nil, ErrInvalidRecord
		}
		return &A{Hdr: h, Addr: addr}, nil
	case TypeAAAA:
		addr, ok := netip.AddrFromSlice(msg[off:end])
		if !ok || addr.Is4() {
			return nil, ErrInvalidRecord
		}
		return &AAAA{Hdr: h, Addr: addr}, nil
	case TypeSRV:
		if off+srvFixedLen >= end {
			return nil, ErrInvalidRecord
		}
		target, _, err := unpackName(msg, off+srvFixedLen)
		if err != nil {
			return nil, err
		}
		return &SRV{
			Hdr:      h,
			Priority: binary.BigEndian.Uint16(msg[off : off+2]),
			Weight:   binary.BigEndian.Uint16(msg[off+2 : off+4]),
			Port:     binary.BigEndian.Uint16(msg[off+4 : off+6]),
			Target:   target,
		}, nil
	default:
		return &Unknown{Hdr: h, Data: bytes.Clone(msg[off:end])}, nil
	}
}
