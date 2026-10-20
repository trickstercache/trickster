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
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamRateLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	echo := newStreamEcho(t, "echo")
	udpOrigin := udpEchoBackend(t, "echo")
	ports, release := portutil.Reserve(t, 5)
	udpPorts, releaseUDP := portutil.ReserveUDP(t, 1)
	listeners := fmt.Sprintf(`  rl-it-stream-tcp:
    address: 127.0.0.1
    protocol: tcp
    port: %d
    rate_limiter_name: rl-it-stream-tcp
  rl-it-stream-count:
    address: 127.0.0.1
    protocol: tcp
    port: %d
    rate_limiter_name: rl-it-stream-count
  rl-it-stream-udp:
    address: 127.0.0.1
    protocol: udp
    port: %d
    rate_limiter_name: rl-it-stream-udp
`, ports[3], ports[4], udpPorts[0])
	preamble := strings.Replace(promstub.Preamble(ports[0], ports[1], ports[2]),
		"listeners:\n", "listeners:\n"+listeners, 1)
	data := preamble + `rate_limiters:
  rl-it-stream-tcp:
    limit: 1
    window: 1m
    unit: connections
  rl-it-stream-count:
    limit: 1
    window: 1m
    unit: connections
    action: count
  rl-it-stream-udp:
    limit: 1
    window: 1m
    unit: sessions
backends:
  rl-it-stream-tcp-db:
    provider: rp
    origin_url: tcp://` + echo.addr + `
    listener_names: [rl-it-stream-tcp]
  rl-it-stream-count-db:
    provider: rp
    origin_url: tcp://` + echo.addr + `
    listener_names: [rl-it-stream-count]
  rl-it-stream-udp-db:
    provider: rp
    origin_url: udp://` + udpOrigin + `
    listener_names: [rl-it-stream-udp]
`
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	release()
	releaseUDP()
	runTrickster(t, context.Background(), "-config", path)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", ports[1])
	waitForTrickster(t, metricsAddr)

	tcpAddr := fmt.Sprintf("127.0.0.1:%d", ports[3])
	limited := streamRateDecisions(t, metricsAddr, "rl-it-stream-tcp", "limited")
	conn := dialStream(t, tcpAddr)
	if _, err := conn.Write([]byte("one\n")); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo:one\n", reply)
	refused, err := net.DialTimeout("tcp", tcpAddr, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = refused.Close() })
	_ = refused.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = refused.Read(make([]byte, 1))
	require.ErrorIs(t, err, syscall.ECONNRESET)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.Greater(collect, streamRateDecisions(t, metricsAddr, "rl-it-stream-tcp", "limited"), limited)
	}, 5*time.Second, 50*time.Millisecond)

	countAddr := fmt.Sprintf("127.0.0.1:%d", ports[4])
	counted := streamRateDecisions(t, metricsAddr, "rl-it-stream-count", "counted")
	for _, msg := range []string{"a\n", "b\n"} {
		c := dialStream(t, countAddr)
		_, err := c.Write([]byte(msg))
		require.NoError(t, err)
		got, err := bufio.NewReader(c).ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "echo:"+msg, got)
	}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.Greater(collect, streamRateDecisions(t, metricsAddr, "rl-it-stream-count", "counted"), counted)
	}, 5*time.Second, 50*time.Millisecond)

	udpAddr := fmt.Sprintf("127.0.0.1:%d", udpPorts[0])
	udpLimited := streamRateDecisions(t, metricsAddr, "rl-it-stream-udp", "limited")
	first, err := net.Dial("udp", udpAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	_, err = first.Write([]byte("one"))
	require.NoError(t, err)
	_ = first.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 32)
	n, err := first.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "echo:one", string(buf[:n]))
	other, err := net.Dial("udp", udpAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	_, err = other.Write([]byte("two"))
	require.NoError(t, err)
	_ = other.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err = other.Read(buf); err == nil {
		t.Fatal("a second udp session was relayed")
	}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.Greater(collect, streamRateDecisions(t, metricsAddr, "rl-it-stream-udp", "limited"), udpLimited)
	}, 5*time.Second, 50*time.Millisecond)
}

func streamRateDecisions(t *testing.T, metricsAddr, limiter, result string) float64 {
	t.Helper()
	v, _ := metricValue(t, metricsAddr, "trickster_ratelimit_decisions_total",
		fmt.Sprintf(`limiter=%q,plane="stream",result=%q`, limiter, result))
	return v
}

func dialStream(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
