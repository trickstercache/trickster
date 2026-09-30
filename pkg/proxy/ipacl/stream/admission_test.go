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

package stream

import (
	"net/netip"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	"github.com/trickstercache/trickster/v2/pkg/proxy/l4"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func list(t *testing.T, o ipacl.Options) *ipacl.List {
	t.Helper()
	compiled, _, err := ipacl.Compile(o)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func flow(protocol, serverName, ip string) l4.Flow {
	addr := netip.MustParseAddr(ip)
	return l4.Flow{
		Protocol: protocol, ServerName: serverName,
		Client: netip.AddrPortFrom(addr, 4242),
	}
}

func TestNewSkipsListsTheRelayAlreadyHandled(t *testing.T) {
	peer := list(t, ipacl.Options{Source: "peer", Allow: []string{"127.0.0.1"}})
	if New(l4.ProtocolTCP, Attached{List: peer}, nil, nil) != nil ||
		New(l4.ProtocolTLS, Attached{List: peer}, nil, nil) != nil {
		t.Fatal("a tcp or tls peer list was handed to admission")
	}
	udp := New(l4.ProtocolUDP, Attached{List: peer}, nil, nil)
	if udp == nil || udp.Datagrams() {
		t.Fatal("udp peer list was not an admission that leaves datagrams alone")
	}
	if _, hold := udp.(l4.Holder); hold {
		t.Fatal("the admission installs its own udp hold")
	}

	up := l4.Static("127.0.0.1:9")
	table := l4.NewTable()
	if err := table.Add("", up); err != nil {
		t.Fatal(err)
	}
	if New(l4.ProtocolTCP, Attached{}, table, map[l4.Upstream]Attached{
		up: {List: list(t, ipacl.Options{Source: "peer"})},
	}) != nil {
		t.Fatal("a backend peer list was handed to admission")
	}
}

func TestAdmissionMapsTheListAction(t *testing.T) {
	client := netip.MustParseAddr("192.0.2.9")
	for _, tc := range []struct {
		name   string
		action string
		want   l4.Verdict
	}{
		{name: "reject", action: "reject", want: l4.Reject},
		{name: "drop", action: "drop", want: l4.Drop},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deny := list(t, ipacl.Options{Action: tc.action})
			allow := list(t, ipacl.Options{Default: "allow", Action: tc.action})
			adm := New(l4.ProtocolTCP, Attached{List: deny}, nil, nil)
			if got := adm.Peer(l4.Flow{Protocol: l4.ProtocolTCP, Client: netip.AddrPortFrom(client, 1)}); got != tc.want {
				t.Fatalf("denied client verdict = %v, want %v", got, tc.want)
			}
			if got := adm.Peer(l4.Flow{Protocol: l4.ProtocolTCP}); got != tc.want {
				t.Fatalf("missing client verdict = %v, want %v", got, tc.want)
			}
			allowed := New(l4.ProtocolTCP, Attached{List: allow}, nil, nil)
			if got := allowed.Peer(l4.Flow{Protocol: l4.ProtocolTCP, Client: netip.AddrPortFrom(client, 1)}); got != l4.Allow {
				t.Fatalf("allowed client verdict = %v", got)
			}
			if got := allowed.Datagram(l4.Flow{}, 1); got != l4.Allow || allowed.Datagrams() {
				t.Fatal("datagram judging is enabled")
			}
		})
	}
}

func TestAdmissionOrdersListenerThenBackend(t *testing.T) {
	up := l4.Static("127.0.0.1:9")
	table := l4.NewTable()
	if err := table.Add("", up); err != nil {
		t.Fatal(err)
	}
	listener := list(t, ipacl.Options{Action: "drop", Allow: []string{"192.0.2.9"}})
	backend := list(t, ipacl.Options{Action: "reject"})
	adm := New(l4.ProtocolTCP, Attached{List: listener}, table, map[l4.Upstream]Attached{up: {List: backend}})

	// the listener allows this client, so the backend's reject is the flow verdict
	allowed := flow(l4.ProtocolTCP, "", "192.0.2.9")
	if got := adm.Peer(allowed); got != l4.Allow {
		t.Fatalf("peer = %v, want allow", got)
	}
	if got := adm.Flow(allowed); got != l4.Reject {
		t.Fatalf("flow = %v, want the backend reject", got)
	}
	// a client the listener denies never reaches the backend's action
	denied := flow(l4.ProtocolTCP, "", "198.51.100.4")
	if got := adm.Peer(denied); got != l4.Drop {
		t.Fatalf("peer = %v, want the listener drop", got)
	}

	udp := New(l4.ProtocolUDP, Attached{List: listener}, table, map[l4.Upstream]Attached{up: {List: backend}})
	if got := udp.Peer(flow(l4.ProtocolUDP, "", "192.0.2.9")); got != l4.Reject {
		t.Fatalf("udp backend verdict = %v, want reject", got)
	}
	if got := udp.Peer(flow(l4.ProtocolUDP, "", "198.51.100.4")); got != l4.Drop {
		t.Fatalf("udp listener verdict = %v, want drop", got)
	}
	if got := udp.Flow(flow(l4.ProtocolUDP, "", "192.0.2.9")); got != l4.Allow {
		t.Fatalf("udp flow = %v", got)
	}
}

func TestTCPPeerListDoesNotJudgeTheProxySource(t *testing.T) {
	up := l4.Static("127.0.0.1:9")
	table := l4.NewTable()
	if err := table.Add("", up); err != nil {
		t.Fatal(err)
	}
	// the socket list would deny the PROXY source; admission must not apply it
	peer := list(t, ipacl.Options{Source: "peer", Allow: []string{"10.1.1.1"}})
	backend := list(t, ipacl.Options{Default: "allow"})
	adm := New(l4.ProtocolTCP, Attached{List: peer}, table, map[l4.Upstream]Attached{up: {List: backend}})
	f := flow(l4.ProtocolTCP, "", "192.0.2.9")
	if got := adm.Peer(f); got != l4.Allow {
		t.Fatalf("peer = %v, want the PROXY source left alone", got)
	}
	if got := adm.Flow(f); got != l4.Allow {
		t.Fatalf("flow = %v", got)
	}
}

func TestTLSBackendListFollowsTableLookup(t *testing.T) {
	exact := l4.Static("127.0.0.1:1")
	wild := l4.Static("127.0.0.1:2")
	catch := l4.Static("127.0.0.1:3")
	table := l4.NewTable()
	for host, up := range map[string]l4.Upstream{
		"shop.example.com": exact,
		"*.example.com":    wild,
		"":                 catch,
	} {
		if err := table.Add(host, up); err != nil {
			t.Fatal(err)
		}
	}
	adm := New(l4.ProtocolTLS, Attached{}, table, map[l4.Upstream]Attached{
		exact: {List: list(t, ipacl.Options{Action: "reject", Allow: []string{"10.0.0.1"}})},
		wild:  {List: list(t, ipacl.Options{Action: "drop", Allow: []string{"127.0.0.1"}})},
		catch: {List: list(t, ipacl.Options{Action: "drop"})},
	})
	if got := adm.Peer(flow(l4.ProtocolTLS, "", "127.0.0.1")); got != l4.Allow {
		t.Fatalf("peer = %v, want allow before the route", got)
	}
	cases := []struct {
		name string
		want l4.Verdict
	}{
		{name: "shop.example.com", want: l4.Reject},
		{name: "Shop.Example.com", want: l4.Reject},
		{name: "other.example.com", want: l4.Allow},
		{name: "a.b.example.com", want: l4.Drop},
		{name: "nope.test", want: l4.Drop},
	}
	for _, tc := range cases {
		got := adm.Flow(flow(l4.ProtocolTLS, tc.name, "127.0.0.1"))
		up := table.Lookup(tc.name)
		if got != tc.want {
			t.Errorf("%s verdict = %v, want %v", tc.name, got, tc.want)
		}
		// the verdict is the list stored on the upstream Lookup returned
		if up == exact && got != l4.Reject || up == wild && got != l4.Allow || up == catch && got != l4.Drop {
			t.Errorf("%s lookup %v disagreed with verdict %v", tc.name, up, got)
		}
	}
}

func TestRejectAndDropCountAsDeny(t *testing.T) {
	client := netip.AddrPortFrom(netip.MustParseAddr("192.0.2.9"), 1)
	for _, action := range []string{"reject", "drop"} {
		name := "stream-" + action
		before := testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(name, metrics.IPACLScopeListener, "deny"))
		adm := New(l4.ProtocolTCP, Attached{
			List: list(t, ipacl.Options{Action: action}), Name: name,
		}, nil, nil)
		got := adm.Peer(l4.Flow{Protocol: l4.ProtocolTCP, Client: client})
		if action == "drop" && got != l4.Drop || action == "reject" && got != l4.Reject {
			t.Fatalf("%s verdict = %v", action, got)
		}
		if after := testutil.ToFloat64(metrics.IPACLDecisions.WithLabelValues(
			name, metrics.IPACLScopeListener, "deny")); after != before+1 {
			t.Fatalf("%s deny = %v, want %v", action, after, before+1)
		}
	}
}
