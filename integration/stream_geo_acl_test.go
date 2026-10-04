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

type geoStreamFixture struct {
	name, protocol, origin, action string
	viaALB                         bool
	relay                          string
}

func startGeoStreams(t *testing.T, fixtures []geoStreamFixture) string {
	t.Helper()
	ports, release := portutil.Reserve(t, 3+len(fixtures))
	udpPorts, releaseUDP := portutil.ReserveUDP(t, 1)
	var listeners, acls, backends strings.Builder
	for i := range fixtures {
		f := &fixtures[i]
		port := ports[3+i]
		if f.protocol == "udp" {
			port = udpPorts[0]
		}
		f.relay = fmt.Sprintf("127.0.0.1:%d", port)
		fmt.Fprintf(&listeners, "  %s:\n    address: 127.0.0.1\n    protocol: %s\n    port: %d\n",
			f.name, f.protocol, port)
		fmt.Fprintf(&acls, "  %s:\n    deny: [FR]\n    action: %s\n", f.name, f.action)
		fmt.Fprintf(&backends, "  %s-db:\n    provider: rp\n    origin_url: %s://%s\n    listener_names: [%s]\n",
			f.name, f.protocol, f.origin, f.name)
		if f.viaALB {
			fmt.Fprintf(&backends, "  %s-pool:\n    provider: alb\n    listener_names: [%s]\n"+
				"    alb:\n      mechanism: rr\n      pool: [%s-db]\n", f.name, f.name, f.name)
		}
		fmt.Fprintf(&backends, "    geo_acl_name: %s\n", f.name)
	}
	preamble := strings.Replace(promstub.Preamble(ports[0], ports[1], ports[2]),
		"listeners:\n", "listeners:\n"+listeners.String(), 1)
	data := preamble + "geo_locators:\n  default:\n    provider: geofeed\n" +
		"    geofeed:\n      entries: [\"127.0.0.1/32,FR\"]\n" +
		"geo_acls:\n" + acls.String() + "backends:\n" + backends.String()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	release()
	releaseUDP()
	runTrickster(t, context.Background(), "-config", path)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", ports[1])
	waitForTrickster(t, metricsAddr)
	return metricsAddr
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
	echoes := []*streamEcho{newStreamEcho(t, "echo"), newStreamEcho(t, "echo"), newStreamEcho(t, "echo")}
	fixtures := []geoStreamFixture{
		{name: "geo-it-stream-tcp", protocol: "tcp", origin: echoes[0].addr, action: "reject"},
		{name: "geo-it-stream-alb", protocol: "tcp", origin: echoes[1].addr, action: "reject", viaALB: true},
		{name: "geo-it-stream-count", protocol: "tcp", origin: echoes[2].addr, action: "count"},
		{name: "geo-it-stream-udp", protocol: "udp", origin: udpEchoBackend(t, "echo"), action: "reject"},
	}
	metricsAddr := startGeoStreams(t, fixtures)
	for i, name := range []string{"tcp refused", "tcp alb refused before a member is dialed", "tcp counted"} {
		t.Run(name, func(t *testing.T) {
			f := fixtures[i]
			verdict := "deny"
			if f.action == "count" {
				verdict = "count"
			}
			before := streamGeoDecisions(t, metricsAddr, f.name, verdict)
			s := &streamLB{addr: f.relay}
			if f.action == "count" {
				require.Equal(t, "echo", s.askAndClose(t))
			} else {
				require.Empty(t, s.askAndClose(t), "a refused client was relayed")
				require.Zero(t, echoes[i].accepted.Load())
			}
			require.Eventually(t, func() bool {
				return streamGeoDecisions(t, metricsAddr, f.name, verdict) == before+1
			}, 10*time.Second, 50*time.Millisecond)
		})
	}
	t.Run("udp refused", func(t *testing.T) {
		f := fixtures[3]
		before := streamGeoDecisions(t, metricsAddr, f.name, "deny")
		conn, err := net.Dial("udp", f.relay)
		require.NoError(t, err)
		defer conn.Close()
		_, err = conn.Write([]byte("hi"))
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return streamGeoDecisions(t, metricsAddr, f.name, "deny") == before+1
		}, 10*time.Second, 50*time.Millisecond)
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err = conn.Read(make([]byte, 64))
		require.Error(t, err, "a refused client got a reply")
	})
}
