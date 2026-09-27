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
package l4

import (
	"net"
	"testing"
)

type admitAll struct{}

func (admitAll) Peer(Flow) Verdict          { return Allow }
func (admitAll) Flow(Flow) Verdict          { return Allow }
func (admitAll) Datagram(Flow, int) Verdict { return Allow }
func (admitAll) Datagrams() bool            { return true }

// the receive path with a session open: a map hit and a queue push, and the same with an admission
// configured, since admissions run on the flows' own workers
func BenchmarkPacketServerForward(b *testing.B) {
	for _, tc := range []struct {
		name string
		adm  Admission
	}{{"unjudged", nil}, {"judged", admitAll{}}} {
		b.Run(tc.name, func(b *testing.B) {
			s := NewPacketServer("bench", &Config{Admission: tc.adm})
			client := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 4000}
			sess := newUDPSession(client)
			sess.state = flowOpen
			s.sessions[client.String()] = sess
			payload := make([]byte, 512)
			b.ReportAllocs()
			for b.Loop() {
				s.forward(payload, client)
				sess.mu.Lock()
				for sess.num > 0 {
					s.queued.Add(-int64(len(sess.pop())))
				}
				sess.mu.Unlock()
			}
		})
	}
}
