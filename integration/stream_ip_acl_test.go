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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

// TestStreamIPACL denies a loopback client at a TCP listener, reloads to allow
// it, then reloads back to deny. The connection opened while the list allowed
// the client keeps its flow; a connection opened after the second reload does not.
func TestStreamIPACL(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	signal.Reset(syscall.SIGHUP)
	members, _ := tcpMembers(t, "echo")
	s := startStreamACL(t, members, "10.0.0.0/8")
	require.Empty(t, s.askAndClose(t), "the loopback client was accepted by an allow list of 10.0.0.0/8")

	rewriteStreamACL(t, s, members, "127.0.0.1/32")
	require.Eventually(t, func() bool { return s.askAndClose(t) == "echo" }, 15*time.Second, 100*time.Millisecond,
		"new connections never observed the allow reload")

	name, held := s.ask(t)
	require.Equal(t, "echo", name)
	t.Cleanup(func() { _ = held.Close() })

	rewriteStreamACL(t, s, members, "10.0.0.0/8")
	require.Eventually(t, func() bool { return s.askAndClose(t) == "" }, 15*time.Second, 100*time.Millisecond,
		"new connections never observed the deny reload")

	_ = held.SetDeadline(time.Now().Add(5 * time.Second))
	_, err := held.Write([]byte("still\n"))
	require.NoError(t, err)
	reply, err := bufio.NewReader(held).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo:still\n", reply, "a connection open across the reload was rejudged")
}

func startStreamACL(t *testing.T, members [][2]string, allow string) *streamLB {
	t.Helper()
	ports, release := portutil.Reserve(t, 4)
	s := &streamLB{
		cfgPath:  filepath.Join(t.TempDir(), "trickster.yaml"),
		protocol: "tcp",
		ports:    ports,
		addr:     fmt.Sprintf("127.0.0.1:%d", ports[3]),
	}
	writeStreamACL(t, s, members, allow)
	release()
	runTrickster(t, context.Background(), "-config", s.cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", ports[1]))
	return s
}

func rewriteStreamACL(t *testing.T, s *streamLB, members [][2]string, allow string) {
	t.Helper()
	writeStreamACL(t, s, members, allow)
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(s.cfgPath, future, future))
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
}

func writeStreamACL(t *testing.T, s *streamLB, members [][2]string, allow string) {
	t.Helper()
	var sb strings.Builder
	fmt.Fprintf(&sb, "ip_acls:\n  relay-acl:\n    source: peer\n    action: reject\n    default: deny\n    allow:\n      - %s\n", allow)
	relay := fmt.Sprintf("listeners:\n  relay:\n    address: 127.0.0.1\n    protocol: tcp\n    port: %d\n"+
		"    ip_acl_name: relay-acl\n    stream:\n      connect_timeout: 2s\n", s.ports[3])
	sb.WriteString(strings.Replace(promstub.Preamble(s.ports[0], s.ports[1], s.ports[2]), "listeners:\n", relay, 1))
	sb.WriteString("backends:\n")
	sb.WriteString("  none:\n    provider: rp\n    origin_url: http://127.0.0.1:1\n")
	for _, m := range members {
		fmt.Fprintf(&sb, "  %s:\n    provider: rp\n    origin_url: tcp://%s\n    listener_names: [relay]\n", m[0], m[1])
	}
	sb.WriteString("  lb:\n    provider: alb\n    listener_names: [relay]\n    alb:\n      mechanism: rr\n      pool:\n")
	for _, m := range members {
		fmt.Fprintf(&sb, "        - %s\n", m[0])
	}
	require.NoError(t, os.WriteFile(s.cfgPath, []byte(sb.String()), 0o644))
}
