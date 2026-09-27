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
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"

	"github.com/stretchr/testify/require"
)

// streamStickyALB is the body of a round-robin ALB that keeps its flows' sessions in a table
const streamStickyALB = "      mechanism: rr\n      sticky: {}\n"

// stickyResults returns how many of the lb ALB's flows the sticky metric counts under result
func (s *streamLB) stickyResults(t *testing.T, result string) float64 {
	t.Helper()
	return metricsutil.Scrape(t, s.ports[1])[metricsutil.Key("trickster_alb_sticky_total",
		map[string]string{"alb_name": "lb", "result": result})]
}

// reload rewrites the config and waits until Trickster has applied it
func (s *streamLB) reload(t *testing.T, members [][2]string, alb string) {
	t.Helper()
	const lastReload = "trickster_config_last_reload_success_time_seconds"
	before := metricsutil.Scrape(t, s.ports[1])[lastReload]
	// the reload is stamped in whole seconds, so it must land in a later one to be told apart
	for time.Now().Unix() <= int64(before) {
		time.Sleep(50 * time.Millisecond)
	}
	s.write(t, members, "", alb)
	// the daemon reloads only a config whose file is newer than the one it loaded
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(s.cfgPath, future, future))
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	require.Eventually(t, func() bool {
		return metricsutil.Scrape(t, s.ports[1])[lastReload] > before
	}, 15*time.Second, 100*time.Millisecond, "the config was never reloaded")
}

// every connection here comes from 127.0.0.1: one client, whose connections all keep to the
// member its first one reached, through a reload that changes the pool around it
func TestStreamStickyKeepsAClientThroughAReload(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	signal.Reset(syscall.SIGHUP)
	members, _ := tcpMembers(t, "a", "b", "c", "d")
	s := startStreamLB(t, "tcp", members[:3], "", streamStickyALB)
	first := s.askAndClose(t)
	require.NotEmpty(t, first)
	for range 10 {
		require.Equal(t, first, s.askAndClose(t), "a round robin moved a pinned client")
	}
	require.EqualValues(t, 1, s.stickyResults(t, "miss"))
	require.EqualValues(t, 10, s.stickyResults(t, "hit"))

	// d joins ahead of the others, which a fresh rotation would start with
	s.reload(t, append(members[3:], members[:3]...), streamStickyALB)
	for range 10 {
		require.Equal(t, first, s.askAndClose(t), "a reload moved a pinned client")
	}
	require.EqualValues(t, 1, s.stickyResults(t, "miss"), "a reload dropped the client's pin")
	entries := metricsutil.Key("trickster_alb_sticky_entries", map[string]string{"alb_name": "lb"})
	require.EqualValues(t, 1, metricsutil.Scrape(t, s.ports[1])[entries])
}

// a udp client's new sessions, each from a new port, land where its first one was answered
func TestStreamStickyUDPSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	members := [][2]string{{"a", udpEchoBackend(t, "a")}, {"b", udpEchoBackend(t, "b")}, {"c", udpEchoBackend(t, "c")}}
	s := startStreamLB(t, "udp", members, "", streamStickyALB)
	var first string
	for i := range 8 {
		conn, err := net.Dial("udp", s.addr)
		require.NoError(t, err)
		_, err = conn.Write([]byte("ping"))
		require.NoError(t, err)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 32)
		n, err := conn.Read(buf)
		require.NoError(t, err, "session %d", i)
		_ = conn.Close()
		name, _, _ := strings.Cut(string(buf[:n]), ":")
		if first == "" {
			first = name
		}
		require.Equal(t, first, name, "session %d from a new port reached another member", i)
	}
}

// a pinned member that goes down moves its client, which then stays where it moved, or, when its
// sessions must not move, has its connections refused until the member is back
func TestStreamStickyWhenThePinnedMemberGoesDown(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	for _, mode := range []string{"repick", "reject"} {
		t.Run(mode, func(t *testing.T) {
			members, echoes := tcpMembers(t, "a", "b", "c")
			alb := fmt.Sprintf("      mechanism: rr\n      stream:\n        connect_retries: 1\n"+
				"      sticky: {on_unavailable: %s}\n", mode)
			s := startStreamLB(t, "tcp", members, "", alb)
			first := s.askAndClose(t)
			require.NotEmpty(t, first)
			echoes[first].stop()
			if mode == "reject" {
				for range 4 {
					require.Empty(t, s.askAndClose(t), "a session that must not move was moved")
				}
				require.EqualValues(t, 4, s.stickyResults(t, "rejected"))
				echoes[first].restart(t)
				require.Equal(t, first, s.askAndClose(t), "the client did not return to its member")
				return
			}
			moved := s.askAndClose(t)
			require.NotEmpty(t, moved, "the retry did not move the session")
			require.NotEqual(t, first, moved)
			echoes[first].restart(t)
			for range 6 {
				require.Equal(t, moved, s.askAndClose(t), "a moved session went back")
			}
			require.EqualValues(t, 1, s.stickyResults(t, "repick"))
		})
	}
}
