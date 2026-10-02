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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4/options"
)

// a reload routes new connections only: one already relayed stays on the member it committed to
func TestServerUpdateRoutesNewConnectionsOnly(t *testing.T) {
	before, after := echoServer(t, "before:", nil), echoServer(t, "after:", nil)
	srv, addr := startServer(t, ProtocolTCP,
		&Config{Table: tableOf(t, map[string]Upstream{"": Static(before)})})
	held := dialTCP(t, addr)
	if got := exchange(t, held, "one"); got != "before:one" {
		t.Fatalf("reply = %q", got)
	}
	srv.Update(&Config{Table: tableOf(t, map[string]Upstream{"": Static(after)})})
	if got := exchange(t, dialTCP(t, addr), "new"); got != "after:new" {
		t.Errorf("a connection accepted after the update = %q", got)
	}
	if got := exchange(t, held, "two"); got != "before:two" {
		t.Errorf("a connection relayed before the update = %q", got)
	}
	// an update that routes nothing refuses new connections and still leaves the held one alone
	srv.Update(nil)
	expectClosed(t, dialTCP(t, addr))
	if got := exchange(t, held, "three"); got != "before:three" {
		t.Errorf("a held connection after an empty update = %q", got)
	}
}

func TestPacketServerUpdateRoutesNewSessionsOnly(t *testing.T) {
	before, after := udpEcho(t, "before:"), udpEcho(t, "after:")
	idle := &options.Options{IdleTimeout: timeconv.Duration(5 * time.Second)}
	srv, addr, _ := startPacketServer(t, &Config{
		Table: tableOf(t, map[string]Upstream{"": Static(before)}), Options: idle,
	})
	held := udpClient(t, addr)
	if got := datagram(t, held, "one"); got != "before:one" {
		t.Fatalf("reply = %q", got)
	}
	srv.Update(&Config{Table: tableOf(t, map[string]Upstream{"": Static(after)}), Options: idle})
	if got := datagram(t, udpClient(t, addr), "new"); got != "after:new" {
		t.Errorf("a session opened after the update = %q", got)
	}
	if got := datagram(t, held, "two"); got != "before:two" {
		t.Errorf("a session opened before the update = %q", got)
	}
}
