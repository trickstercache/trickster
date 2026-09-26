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
package flowkey
import (
	"net/netip"
	"testing"
)

const sessionUser = "alice"

func sessionFor(t *testing.T, source string) func(user string, client netip.Addr) Value {
	t.Helper()
	ks := sourcesOf(t, source)[0]
	return func(user string, client netip.Addr) Value { return Session(ks, DefaultIPv6Prefix, user, client) }
}

func TestSessionValue(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.1")
	user := sessionFor(t, "user")
	if a := user(sessionUser, v4); !a.OK || a != user(sessionUser, netip.Addr{}) || a == user("bob", v4) {
		t.Error("a user key does not follow the name alone")
	}
	if user("", v4).OK {
		t.Error("a session with no name has a user key")
	}
	client := sessionFor(t, "client_ip")
	if a := client(sessionUser, v4); !a.OK || a != client("bob", v4) {
		t.Error("a client_ip key does not follow the address alone")
	}
	if client(sessionUser, netip.Addr{}).OK {
		t.Error("a session with no address has a client_ip key")
	}
	mapped := netip.MustParseAddr("::ffff:192.0.2.1")
	if client("", mapped) != client("", v4) {
		t.Error("an IPv4-mapped address keys apart from its IPv4 form")
	}
	a := client("", netip.MustParseAddr("2001:db8:1:2:aaaa:bbbb:cccc:dddd"))
	if a != client("", netip.MustParseAddr("2001:db8:1:2::1")) {
		t.Error("two addresses in one /64 keyed differently")
	}
}

func TestUnreadableSessionSourcesAreNeverPresent(t *testing.T) {
	for _, source := range []string{"host", "sni", "header:X-Tenant", "cookie:s", "query:q", "proxy_tlv:0xEA", "path"} {
		if sessionFor(t, source)(sessionUser, netip.MustParseAddr("192.0.2.1")).OK {
			t.Errorf("a session carries %s", source)
		}
	}
}

func TestSessionExtractionDoesNotAllocate(t *testing.T) {
	client := netip.MustParseAddr("2001:db8::1")
	for _, source := range []string{"user", "client_ip"} {
		key := sessionFor(t, source)
		if allocs := testing.AllocsPerRun(200, func() { _ = key(sessionUser, client) }); allocs != 0 {
			t.Errorf("%s allocates %v times", source, allocs)
		}
	}
}
