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
	"errors"
	"testing"
)

func TestParseKeySource(t *testing.T) {
	for in, want := range map[string]KeySource{
		"":                  {Kind: KeyClientIP},
		"client_ip":         {Kind: KeyClientIP},
		"  host ":           {Kind: KeyHost},
		"header:x-tenant":   {Kind: KeyHeader, Name: "X-Tenant"},
		"header: X-Tenant ": {Kind: KeyHeader, Name: "X-Tenant"},
		"cookie:session":    {Kind: KeyCookie, Name: "session"},
		"query:tenant":      {Kind: KeyQuery, Name: "tenant"},
		"sni":               {Kind: KeySNI},
		"user":              {Kind: KeyUser},
		"method":            {Kind: KeyMethod},
		"path":              {Kind: KeyPath},
		"query":             {Kind: KeyRawQuery},
		"proxy_tlv:0xEA":    {Kind: KeyProxyTLV, TLV: 0xEA},
		"proxy_tlv: 5":      {Kind: KeyProxyTLV, TLV: 5},
	} {
		got, err := ParseKeySource(in)
		if err != nil || got != want {
			t.Errorf("ParseKeySource(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"snI:x", "header:", "cookie: ", "query:", "query:a=b", "header:two words",
		"cookie:a;b", "ip", "header", "path:/x", "method:GET", "proxy_tlv:", "proxy_tlv:256", "proxy_tlv:-1",
		"proxy_tlv:authority",
	} {
		if _, err := ParseKeySource(in); !errors.Is(err, ErrInvalidKeySource) {
			t.Errorf("ParseKeySource(%q) = %v; want ErrInvalidKeySource", in, err)
		}
	}
}

func TestKeySourcePlanes(t *testing.T) {
	// what a request must have been through before the key can be read: nothing unless listed
	requires := map[string]Requirement{KeySourceUser: RequiresPrincipal}
	for in, want := range map[string][5]bool{
		// readable on: a tcp or udp listener, a tls listener, one that accepts the PROXY
		// protocol, an http listener, a native protocol listener
		"client_ip":       {true, true, true, true, true},
		"sni":             {false, true, false, false, false},
		"proxy_tlv:0xEA":  {false, false, true, false, false},
		"user":            {false, false, false, false, true},
		"host":            {false, false, false, true, false},
		"header:X-Tenant": {false, false, false, true, false},
		"cookie:session":  {false, false, false, true, false},
		"query:tenant":    {false, false, false, true, false},
		"method":          {false, false, false, true, false},
		"path":            {false, false, false, true, false},
		"query":           {false, false, false, true, false},
	} {
		ks, err := ParseKeySource(in)
		if err != nil {
			t.Fatal(err)
		}
		got := [5]bool{
			ks.OnStream(StreamListener{}), ks.OnStream(StreamListener{TLS: true}),
			ks.OnStream(StreamListener{ProxyProtocol: true}), ks.OnHTTP(), ks.OnNative(),
		}
		if got != want {
			t.Errorf("%s is readable on %v; want %v", in, got, want)
		}
		if got := ks.Requires(); got != requires[in] {
			t.Errorf("%s requires %b; want %b", in, got, requires[in])
		}
	}
}

func TestRequirementHas(t *testing.T) {
	both := RequiresPrincipal | RequiresBody
	if !both.Has(RequiresPrincipal) || !both.Has(both) || RequiresPrincipal.Has(RequiresBody) ||
		!Requirement(0).Has(0) {
		t.Error("a set of requirements misreports what it has")
	}
}
