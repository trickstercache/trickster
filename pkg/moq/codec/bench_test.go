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
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

const (
	benchObjects     = 64
	benchPayloadSize = 1024
)

func benchSubgroupStream(b *testing.B, c *Codec) []byte {
	b.Helper()
	h := message.SubgroupHeader{TrackAlias: 1, Group: 1 << 20, Mode: message.SubgroupIDZero, Priority: 64}
	w := c.NewSubgroupWriter(h)
	buf := c.AppendSubgroupHeader(nil, &h)
	payload := make([]byte, benchPayloadSize)
	var err error
	for i := range benchObjects {
		o := message.SubgroupObject{ID: uint64(i), PayloadLen: benchPayloadSize}
		if buf, err = w.AppendObject(buf, &o); err != nil {
			b.Fatal(err)
		}
		buf = append(buf, payload...)
	}
	return buf
}

func BenchmarkSubgroupObjectDecode(b *testing.B) {
	c := mustCodec(b, 22)
	data := benchSubgroupStream(b, c)
	src := bytes.NewReader(data)
	r := bufio.NewReaderSize(src, 32*1024)
	payload := make([]byte, benchPayloadSize)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		src.Reset(data)
		r.Reset(src)
		typ, _ := wire.ReadVarintFrom(r)
		sr, err := c.ReadSubgroupHeader(typ, r)
		if err != nil {
			b.Fatal(err)
		}
		for {
			o, err := sr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			if _, err = io.ReadFull(r, payload[:o.PayloadLen]); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkSubgroupObjectEncode(b *testing.B) {
	c := mustCodec(b, 22)
	h := message.SubgroupHeader{TrackAlias: 1, Group: 1 << 20, Mode: message.SubgroupIDZero, Priority: 64}
	buf := make([]byte, 0, 1024)
	b.ReportAllocs()
	for b.Loop() {
		w := SubgroupWriter{c: c, header: h}
		buf = c.AppendSubgroupHeader(buf[:0], &h)
		var err error
		for i := range benchObjects {
			o := message.SubgroupObject{ID: uint64(i), PayloadLen: benchPayloadSize}
			if buf, err = w.AppendObject(buf, &o); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkFetchObjectDecode(b *testing.B) {
	c := mustCodec(b, 22)
	w := c.NewFetchWriter(message.GroupOrderAscending)
	var data []byte
	var err error
	for i := range benchObjects {
		o := message.FetchObject{Group: uint64(i / 8), Object: uint64(i % 8), Priority: 1, PayloadLen: 0}
		if data, err = w.AppendObject(data, &o); err != nil {
			b.Fatal(err)
		}
	}
	src := bytes.NewReader(data)
	r := bufio.NewReader(src)
	b.ReportAllocs()
	for b.Loop() {
		src.Reset(data)
		r.Reset(src)
		fr := FetchReader{c: c, r: r, s: fetchState{order: message.GroupOrderAscending}}
		for {
			if _, err := fr.Next(); err != nil {
				break
			}
		}
	}
}

func BenchmarkParseSubscribe(b *testing.B) {
	c := mustCodec(b, 22)
	msg, err := c.AppendMessage(nil, &message.Subscribe{Track: track("video", "live", "sports"), Params: fullParams()})
	if err != nil {
		b.Fatal(err)
	}
	typ, n := wire.ReadVarint(msg)
	body := msg[n+2:]
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.ParseMessage(typ, body); err != nil {
			b.Fatal(err)
		}
	}
}
