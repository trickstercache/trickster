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

package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dns_srv / dns_a scenarios run against the CoreDNS integration
// container (see the "-- INTEGRATION CONTAINERS BELOW --" section of the
// developer compose file), which serves the mutable trickster.test zone
// from the gitignored coredns-zones directory. `make integration-start`
// enables the container and seeds the zone. When CoreDNS is not running,
// these tests skip -- unless TRICKSTER_DNS_TEST=1 (set in CI) makes its
// absence a failure.

const (
	coreDNSAddr = "127.0.0.1:5399"
	coreDNSZone = "../docs/developer/environment/docker-compose-data/coredns-zones/trickster.test.db"
)

// requireCoreDNS skips (or fails, under TRICKSTER_DNS_TEST=1) when the
// CoreDNS integration container is not answering
func requireCoreDNS(t *testing.T) {
	t.Helper()
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, coreDNSAddr)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := r.LookupHost(ctx, "ns.trickster.test"); err != nil {
		if os.Getenv("TRICKSTER_DNS_TEST") == "1" {
			t.Fatalf("TRICKSTER_DNS_TEST=1 but CoreDNS is not answering at %s: %v",
				coreDNSAddr, err)
		}
		t.Skipf("CoreDNS integration container not running at %s "+
			"(run `make integration-start`); skipping", coreDNSAddr)
	}
}

// zoneSerial produces strictly-increasing SOA serials that fit the
// 32-bit serial field (a UnixNano serial silently breaks zone parsing)
var zoneSerial atomic.Int64

// writeZone atomically replaces the trickster.test zone with the provided
// records, bumping the SOA serial so CoreDNS's file-plugin reload picks up
// the change
func writeZone(t *testing.T, records ...string) {
	t.Helper()
	if zoneSerial.Load() == 0 {
		zoneSerial.CompareAndSwap(0, time.Now().Unix())
	}
	serial := zoneSerial.Add(1)
	var b strings.Builder
	fmt.Fprintf(&b, "$ORIGIN trickster.test.\n$TTL 1\n")
	fmt.Fprintf(&b,
		"@\tIN\tSOA\tns.trickster.test. admin.trickster.test. %d 60 60 604800 1\n",
		serial)
	b.WriteString("@\tIN\tNS\tns.trickster.test.\nns\tIN\tA\t127.0.0.1\n")
	for _, r := range records {
		b.WriteString(r + "\n")
	}
	tmp := coreDNSZone + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(b.String()), 0o644))
	require.NoError(t, os.Rename(tmp, coreDNSZone))
}

func TestALBDiscoveryDNSSRV(t *testing.T) {
	requireCoreDNS(t)
	const (
		frontPort   = 19520
		metricsPort = 19521
		mgmtPort    = 19522
	)
	leafA := newDiscoveryLeaf(t, "leafA")
	leafB := newDiscoveryLeaf(t, "leafB")

	// two SRV targets on localhost with weights 1 and 3
	writeZone(t,
		fmt.Sprintf("_web._tcp\tIN\tSRV\t10 1 %s localhost.", leafA.port()),
		fmt.Sprintf("_web._tcp\tIN\tSRV\t10 3 %s localhost.", leafB.port()),
	)

	cfg := discoveryALBConfig(frontPort, metricsPort, mgmtPort,
		"  d1:\n    provider: dns_srv\n    dns:\n      resolver: "+
			coreDNSAddr+"\n      interval: 1s",
		"          srv_name: _web._tcp.trickster.test")
	startDiscoveryTrickster(t, cfg)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)

	// SRV weights drive exact 1:3 round-robin apportionment
	frontURL := fmt.Sprintf("http://127.0.0.1:%d/", frontPort)
	leafA.hits.Store(0)
	leafB.hits.Store(0)
	const cycles = 5
	for range cycles * 4 {
		status, _ := getBody(t, frontURL)
		require.Equal(t, http.StatusOK, status)
	}
	require.Equal(t, int64(cycles), leafA.hits.Load(),
		"weight-1 member gets 1 of every 4 requests")
	require.Equal(t, int64(cycles*3), leafB.hits.Load(),
		"weight-3 member gets 3 of every 4 requests")

	// record mutation mid-test: drop leafA from the answer
	writeZone(t,
		fmt.Sprintf("_web._tcp\tIN\tSRV\t10 3 %s localhost.", leafB.port()))
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 1)
	aHits := leafA.hits.Load()
	for range 8 {
		status, body := getBody(t, frontURL)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "leafB", body)
	}
	require.Equal(t, aHits, leafA.hits.Load(),
		"removed SRV target must no longer receive traffic")
}

func TestALBDiscoveryDNSA(t *testing.T) {
	requireCoreDNS(t)
	const (
		frontPort   = 19530
		metricsPort = 19531
		mgmtPort    = 19532
	)
	leaf := newDiscoveryLeaf(t, "leafA")

	writeZone(t, "web\tIN\tA\t127.0.0.1")

	cfg := discoveryALBConfig(frontPort, metricsPort, mgmtPort,
		"  d1:\n    provider: dns_a\n    dns:\n      resolver: "+
			coreDNSAddr+"\n      interval: 1s",
		"          hostname: web.trickster.test\n          port: \""+
			leaf.port()+"\"")
	startDiscoveryTrickster(t, cfg)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 1)

	frontURL := fmt.Sprintf("http://127.0.0.1:%d/", frontPort)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		status, body := getBody(t, frontURL)
		assert.Equal(collect, http.StatusOK, status)
		assert.Equal(collect, "leafA", body)
	}, 15*time.Second, 100*time.Millisecond)

	// rotation: an additional address joins the answer (membership is
	// asserted via the gauge; 10.255.255.1 is intentionally unreachable
	// and receives no traffic assertions)
	writeZone(t,
		"web\tIN\tA\t127.0.0.1",
		"web\tIN\tA\t10.255.255.1")
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)

	// ...and rotates away again
	writeZone(t, "web\tIN\tA\t127.0.0.1")
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 1)
	status, body := getBody(t, frontURL)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "leafA", body)
}
