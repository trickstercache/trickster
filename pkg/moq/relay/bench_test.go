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

package relay_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
)

const benchObjectSize = 1024

type sinkConn struct {
	*memconn.Conn
	opened  atomic.Int32
	written *atomic.Int64
}

func (c *sinkConn) OpenUniStreamSync(ctx context.Context) (session.SendStream, error) {
	// the control stream passes through; data streams are counted without copying what they carry
	if c.opened.Add(1) == 1 {
		return c.Conn.OpenUniStreamSync(ctx)
	}
	sctx, cancel := context.WithCancel(context.Background())
	return &sinkStream{n: c.written, ctx: sctx, cancel: cancel}, nil
}

type sinkStream struct {
	n      *atomic.Int64
	ctx    context.Context
	cancel context.CancelFunc
}

func (s *sinkStream) Write(p []byte) (int, error) {
	s.n.Add(int64(len(p)))
	return len(p), nil
}

func (s *sinkStream) Close() error                     { s.cancel(); return nil }
func (s *sinkStream) CancelWrite(uint64)               { s.cancel() }
func (s *sinkStream) SetWriteDeadline(time.Time) error { return nil }
func (s *sinkStream) SetPriority(int8, bool)           {}
func (s *sinkStream) SetReliableBoundary()             {}
func (s *sinkStream) Context() context.Context         { return s.ctx }

func BenchmarkFanOut(b *testing.B) {
	for _, subscribers := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("subscribers=%d", subscribers), func(b *testing.B) {
			benchFanOut(b, subscribers)
		})
	}
}

func benchFanOut(b *testing.B, subscribers int) {
	h := newHarness(b, relay.Options{}, nil)
	var written atomic.Int64
	for range subscribers {
		cl := h.connect(nil, func(c *memconn.Conn) session.Conn { return &sinkConn{Conn: c, written: &written} })
		req, _ := subscribe(b, cl, "video", message.Params{})
		subscribed(b, req)
	}
	payload := make([]byte, benchObjectSize)
	outs := h.o.stream("video", 1)
	if len(outs) != 1 {
		b.Fatalf("%d upstream streams", len(outs))
	}
	out := outs[0]
	obj := message.SubgroupObject{PayloadLen: benchObjectSize}
	header := int64(0)
	b.SetBytes(benchObjectSize)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		obj.ID = uint64(i)
		if err := out.WriteObject(&obj); err != nil {
			b.Fatal(err)
		}
		if _, err := out.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
	// each subscriber's stream carries its header, then every object's header and payload
	want := int64(subscribers) * int64(b.N) * benchObjectSize
	deadline := time.Now().Add(time.Minute)
	for written.Load()-header < want {
		if time.Now().After(deadline) {
			b.Fatalf("forwarded %d of %d bytes", written.Load(), want)
		}
		time.Sleep(50 * time.Microsecond)
	}
	b.StopTimer()
	_ = out.Close()
}
