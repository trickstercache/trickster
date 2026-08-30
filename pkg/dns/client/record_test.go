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
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func packOne(t *testing.T, rr Record) []byte {
	t.Helper()
	b, err := packRecord(nil, rr)
	require.NoError(t, err)
	return b
}

func TestRecordRoundTrip(t *testing.T) {
	hdr := func(qtype Type) RecordHeader {
		return RecordHeader{Name: "example.com.", Type: qtype, Class: ClassINET, TTL: 60}
	}
	tests := map[string]Record{
		"A":    &A{Hdr: hdr(TypeA), Addr: netip.MustParseAddr("10.0.0.1")},
		"AAAA": &AAAA{Hdr: hdr(TypeAAAA), Addr: netip.MustParseAddr("2001:db8::1")},
		"SRV": &SRV{Hdr: hdr(TypeSRV), Priority: 10, Weight: 5, Port: 9090,
			Target: "prom-a.example.com."},
		"unrecognized type": &Unknown{Hdr: hdr(99), Data: []byte{1, 2, 3}},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			b := packOne(t, want)
			got, next, err := unpackRecord(b, 0)
			require.NoError(t, err)
			require.Equal(t, len(b), next)
			require.Equal(t, want, got)
			require.Equal(t, uint32(60), got.Header().TTL)
		})
	}
}

func TestRecordPackErrors(t *testing.T) {
	_, err := packRecord(nil, &A{Hdr: RecordHeader{Name: "a..b."}})
	require.ErrorIs(t, err, ErrInvalidName)

	_, err = packRecord(nil, &A{
		Hdr:  RecordHeader{Name: "example.com."},
		Addr: netip.MustParseAddr("2001:db8::1"),
	})
	require.ErrorIs(t, err, ErrInvalidRecord, "an A record must hold an IPv4 address")

	_, err = packRecord(nil, &AAAA{
		Hdr:  RecordHeader{Name: "example.com."},
		Addr: netip.MustParseAddr("10.0.0.1"),
	})
	require.ErrorIs(t, err, ErrInvalidRecord, "an AAAA record must hold an IPv6 address")

	_, err = packRecord(nil, &AAAA{Hdr: RecordHeader{Name: "example.com."}})
	require.ErrorIs(t, err, ErrInvalidRecord, "the zero address is not encodable")

	_, err = packRecord(nil, &SRV{Hdr: RecordHeader{Name: "example.com."}, Target: "a..b."})
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestUnpackRecordErrors(t *testing.T) {
	valid := packOne(t, &A{
		Hdr:  RecordHeader{Name: "example.com.", Type: TypeA, Class: ClassINET},
		Addr: netip.MustParseAddr("10.0.0.1"),
	})

	_, _, err := unpackRecord(valid[:len(valid)-1], 0)
	require.ErrorIs(t, err, ErrShortMessage, "the record data is one byte short")

	_, _, err = unpackRecord(valid[:len(valid)-6], 0)
	require.ErrorIs(t, err, ErrShortMessage, "the fixed fields are incomplete")

	_, _, err = unpackRecord([]byte{0x40}, 0)
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestUnpackRDataErrors(t *testing.T) {
	badLen := func(qtype Type, rdLen int) error {
		h := RecordHeader{Name: "example.com.", Type: qtype, Class: ClassINET}
		_, err := unpackRData(h, make([]byte, rdLen), 0, rdLen)
		return err
	}
	require.ErrorIs(t, badLen(TypeA, 3), ErrInvalidRecord)
	require.ErrorIs(t, badLen(TypeA, 16), ErrInvalidRecord,
		"a 16-byte address is not an A record")
	require.ErrorIs(t, badLen(TypeAAAA, 4), ErrInvalidRecord,
		"a 4-byte address is not an AAAA record")
	require.ErrorIs(t, badLen(TypeSRV, srvFixedLen), ErrInvalidRecord,
		"an SRV record needs a target after its fixed fields")
	require.ErrorIs(t, badLen(TypeSRV, 2), ErrInvalidRecord)

	// an SRV target that is not a decodable name
	h := RecordHeader{Name: "example.com.", Type: TypeSRV, Class: ClassINET}
	data := make([]byte, srvFixedLen+1)
	data[srvFixedLen] = 0x40
	_, err := unpackRData(h, data, 0, len(data))
	require.ErrorIs(t, err, ErrInvalidName)
}

func TestUnknownRecordIsCopied(t *testing.T) {
	h := RecordHeader{Name: "example.com.", Type: 99, Class: ClassINET}
	msg := []byte{1, 2, 3}
	rr, err := unpackRData(h, msg, 0, len(msg))
	require.NoError(t, err)
	msg[0] = 9
	require.Equal(t, []byte{1, 2, 3}, rr.(*Unknown).Data,
		"record data must not alias the message buffer")
}
