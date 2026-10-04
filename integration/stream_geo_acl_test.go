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

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

func startGeoStream(t *testing.T, protocol, origin, aclName, action string, viaALB bool) (relay, metricsAddr string) {
	t.Helper()
	// one stream listener relays to origin through a backend, or an alb over it, whose geo ACL denies France,
	// where the geofeed places the loopback address
	ports, release := portutil.Reserve(t, 4)
	relayPort, releaseRelay := ports[3], func() {}
	if protocol == "udp" {
		var udp []int
		udp, releaseRelay = portutil.ReserveUDP(t, 1)
		relayPort = udp[0]
	}
	var sb strings.Builder
	listeners := fmt.Sprintf("listeners:\n  relay:\n    address: 127.0.0.1\n    protocol: %s\n    port: %d\n",
		protocol, relayPort)
	sb.WriteString(strings.Replace(promstub.Preamble(ports[0], ports[1], ports[2]), "listeners:\n", listeners, 1))
	sb.WriteString("geo_locators:\n  default:\n    provider: geofeed\n    geofeed:\n      entries: [\"127.0.0.1/32,FR\"]\n")
	fmt.Fprintf(&sb, "geo_acls:\n  %s:\n    deny: [FR]\n    action: %s\n", aclName, action)
	fmt.Fprintf(&sb, "backends:\n  db:\n    provider: rp\n    origin_url: %s://%s\n    listener_names: [relay]\n",
		protocol, origin)
	if viaALB {
		sb.WriteString("  pool:\n    provider: alb\n    listener_names: [relay]\n    alb:\n      mechanism: rr\n" +
			"      pool: [db]\n")
	}
	fmt.Fprintf(&sb, "    geo_acl_name: %s\n", aclName)
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(sb.String()), 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release()
	releaseRelay()
	runTrickster(t, ctx, "-config", path)
	metricsAddr = fmt.Sprintf("127.0.0.1:%d", ports[1])
	waitForTrickster(t, metricsAddr)
	return fmt.Sprintf("127.0.0.1:%d", relayPort), metricsAddr
}

func streamGeoDecisions(t *testing.T, metricsAddr, aclName, verdict string) float64 {
	t.Helper()
	v, _ := metricValue(t, metricsAddr, geoDecisions, fmt.Sprintf(`geo_acl=%q,plane="stream",verdict=%q`,
		aclName, verdict))
	return v
}

func TestGeoACLStream(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	t.Run("tcp refused", func(t *testing.T) {
		const aclName = "geo-it-stream-tcp"
		echo := newStreamEcho(t, "echo")
		relay, metricsAddr := startGeoStream(t, "tcp", echo.addr, aclName, "reject", false)
		s := &streamLB{addr: relay}
		require.Empty(t, s.askAndClose(t), "a refused client was relayed")
		require.Zero(t, echo.accepted.Load())
		require.Eventually(t, func() bool { return streamGeoDecisions(t, metricsAddr, aclName, "deny") == 1 },
			10*time.Second, 50*time.Millisecond)
	})

	t.Run("tcp alb refused before a member is dialed", func(t *testing.T) {
		const aclName = "geo-it-stream-alb"
		echo := newStreamEcho(t, "echo")
		relay, metricsAddr := startGeoStream(t, "tcp", echo.addr, aclName, "reject", true)
		s := &streamLB{addr: relay}
		require.Empty(t, s.askAndClose(t), "a refused client was relayed")
		require.Zero(t, echo.accepted.Load())
		require.Eventually(t, func() bool { return streamGeoDecisions(t, metricsAddr, aclName, "deny") == 1 },
			10*time.Second, 50*time.Millisecond)
	})

	t.Run("tcp counted", func(t *testing.T) {
		const aclName = "geo-it-stream-count"
		echo := newStreamEcho(t, "echo")
		relay, metricsAddr := startGeoStream(t, "tcp", echo.addr, aclName, "count", false)
		s := &streamLB{addr: relay}
		require.Equal(t, "echo", s.askAndClose(t))
		require.Eventually(t, func() bool { return streamGeoDecisions(t, metricsAddr, aclName, "count") == 1 },
			10*time.Second, 50*time.Millisecond)
	})

	t.Run("udp refused", func(t *testing.T) {
		const aclName = "geo-it-stream-udp"
		relay, metricsAddr := startGeoStream(t, "udp", udpEchoBackend(t, "echo"), aclName, "reject", false)
		conn, err := net.Dial("udp", relay)
		require.NoError(t, err)
		defer conn.Close()
		_, err = conn.Write([]byte("hi"))
		require.NoError(t, err)
		require.Eventually(t, func() bool { return streamGeoDecisions(t, metricsAddr, aclName, "deny") == 1 },
			10*time.Second, 50*time.Millisecond)
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err = conn.Read(make([]byte, 64))
		require.Error(t, err, "a refused client got a reply")
	})
}
