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

package session_test

import (
	"context"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
)

const benchPayload = 1024

type drain struct {
	done chan int
	got  chan *message.Datagram
	buf  []byte
}

func (d *drain) HandleRequest(r *session.Request)                     { r.SetHandlers(d, d) }
func (d *drain) HandleGoAway(*session.Session, *message.GoAway)       {}
func (d *drain) HandleMessage(*session.Request, message.Message)      {}
func (d *drain) HandleClose(*session.Request, error)                  {}
func (d *drain) HandleFetch(*session.Request, *session.IncomingFetch) {}
func (d *drain) HandleDatagram(*session.Request, *message.Datagram)   {}

func (d *drain) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	var n int
	for {
		if _, err := in.Next(); err != nil {
			break
		}
		for {
			m, err := in.Read(d.buf)
			n += m
			if err != nil {
				break
			}
		}
	}
	d.done <- n
}

func benchSessions(b *testing.B) (*session.Request, *drain) {
	b.Helper()
	sh := newRecorder()
	d := &drain{done: make(chan int, 1), buf: make([]byte, 4096)}
	cc, sc := memconn.Pair()
	cc.Capacity, sc.Capacity = 1<<16, 1<<16
	server, _ := session.New(sc, session.Options{Role: session.RoleServer, Codec: c22(b), Handler: sh})
	client, _ := session.New(cc, session.Options{Role: session.RoleClient, Codec: c22(b), Handler: d})
	errs := make(chan error, 2)
	go func() { errs <- server.Start(context.Background()) }()
	go func() { errs <- client.Start(context.Background()) }()
	for range 2 {
		if err := <-errs; err != nil {
			b.Fatal(err)
		}
	}
	b.Cleanup(func() {
		_ = client.Close(message.CodeNoError, "")
		_ = server.Close(message.CodeNoError, "")
	})
	r, err := client.Open(context.Background(), &message.Subscribe{Track: track("v", "ns")}, d, d)
	if err != nil {
		b.Fatal(err)
	}
	in := <-sh.requests
	if err = in.Send(&message.SubscribeOK{TrackAlias: server.NewTrackAlias()}); err != nil {
		b.Fatal(err)
	}
	if _, err = r.Response(context.Background()); err != nil {
		b.Fatal(err)
	}
	return in, d
}

func BenchmarkSubgroupObjects(b *testing.B) {
	in, d := benchSessions(b)
	out, err := in.OpenSubgroup(context.Background(), message.SubgroupHeader{Group: 1, DefaultPriority: true}, 3, false)
	if err != nil {
		b.Fatal(err)
	}
	payload := make([]byte, benchPayload)
	obj := message.SubgroupObject{PayloadLen: benchPayload}
	b.SetBytes(benchPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		obj.ID = uint64(i)
		if err = out.WriteObject(&obj); err != nil {
			b.Fatal(err)
		}
		if _, err = out.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
	_ = out.Close()
	if n := <-d.done; n != b.N*benchPayload {
		b.Fatalf("drained %d bytes", n)
	}
}

func BenchmarkDatagrams(b *testing.B) {
	in, _ := benchSessions(b)
	dg := message.Datagram{Group: 1, Payload: make([]byte, benchPayload)}
	b.SetBytes(benchPayload)
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		dg.Object = uint64(i)
		if err := in.SendDatagram(&dg); err != nil {
			b.Fatal(err)
		}
	}
}
