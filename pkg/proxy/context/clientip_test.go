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

package context

import (
	"context"
	"testing"
)

func TestClientIP(t *testing.T) {
	const ip = "203.0.113.9"
	if got := ClientIP(nil); got != "" {
		t.Errorf("ClientIP(nil) = %q", got)
	}
	ctx := WithClientIP(context.Background(), ip)
	if got := ClientIP(ctx); got != ip {
		t.Errorf("ClientIP = %q, want %q", got, ip)
	}
	if got := ClientIP(WithClientIPOf(context.Background(), ctx)); got != ip {
		t.Errorf("copied ClientIP = %q, want %q", got, ip)
	}
	empty := context.Background()
	if got := WithClientIPOf(empty, empty); got != empty {
		t.Error("a context with no client IP to copy was wrapped")
	}
}

func TestPeerTrusted(t *testing.T) {
	const ip = "203.0.113.9"
	if PeerTrusted(nil) || PeerTrusted(context.Background()) {
		t.Error("a context with no resolved client trusts its peer")
	}
	if PeerTrusted(WithClientIP(context.Background(), ip)) {
		t.Error("an address recorded alone trusts its peer")
	}
	ctx := WithResolvedClient(context.Background(), ip, true)
	if !PeerTrusted(ctx) || ClientIP(ctx) != ip {
		t.Error("the resolved client was not recorded")
	}
	if !PeerTrusted(WithClientIPOf(context.Background(), ctx)) {
		t.Error("a copy lost whether its peer is trusted")
	}
	// recording the peer's trust costs nothing over recording the address
	alone := testing.AllocsPerRun(100, func() { _ = WithClientIP(context.Background(), ip) })
	both := testing.AllocsPerRun(100, func() { _ = WithResolvedClient(context.Background(), ip, true) })
	if both > alone {
		t.Errorf("recording the resolved client allocates %v times, the address alone %v", both, alone)
	}
}
