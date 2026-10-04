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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const streamACLReloadSuccessesMetric = "trickster_config_reload_successes_total"

func TestStreamIPACL(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	guardSIGHUP(t)
	members, _ := tcpMembers(t, "echo")
	s := startStreamACL(t, members, "10.0.0.0/8")
	name, err := probeStreamACL(s.addr)
	require.NoError(t, err)
	require.Empty(t, name, "the loopback client was accepted by an allow list of 10.0.0.0/8")

	rewriteStreamACL(t, s, members, "127.0.0.1/32")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		name, err := probeStreamACL(s.addr)
		assert.NoError(collect, err)
		assert.Equal(collect, "echo", name)
	}, 15*time.Second, 100*time.Millisecond,
		"new connections never observed the allow reload")

	name, held := s.ask(t)
	require.Equal(t, "echo", name)
	t.Cleanup(func() { _ = held.Close() })

	rewriteStreamACL(t, s, members, "10.0.0.0/8")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		name, err := probeStreamACL(s.addr)
		assert.NoError(collect, err)
		assert.Empty(collect, name)
	}, 15*time.Second, 100*time.Millisecond,
		"new connections never observed the deny reload")

	_ = held.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = held.Write([]byte("still\n"))
	require.NoError(t, err)
	reply, err := bufio.NewReader(held).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo:still\n", reply, "a connection open across the reload was rejudged")
}

func probeStreamACL(addr string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		defer conn.Close()
		err = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if err == nil {
			_, err = conn.Write([]byte("hi\n"))
		}
		if err == nil {
			var reply string
			reply, err = bufio.NewReader(conn).ReadString('\n')
			if err == nil {
				name, _, _ := strings.Cut(reply, ":")
				return name, nil
			}
		}
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, io.EOF) {
		return "", nil
	}
	return "", err
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
	before := metricsutil.Scrape(t, s.ports[1])[streamACLReloadSuccessesMetric]
	writeStreamACL(t, s, members, allow)
	future := time.Now().Add(2 * time.Second)
	require.NoError(t, os.Chtimes(s.cfgPath, future, future))
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", s.ports[1])
	sighupUntilReloaded(t, metricsAddr)
	client := &http.Client{Timeout: 2 * time.Second}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, err := client.Get("http://" + metricsAddr + "/metrics")
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		if !assert.Equal(collect, http.StatusOK, resp.StatusCode) {
			return
		}
		scraped, err := metricsutil.Parse(resp.Body)
		if assert.NoError(collect, err) {
			assert.Greater(collect, scraped[streamACLReloadSuccessesMetric], before)
		}
	}, 15*time.Second, 100*time.Millisecond, "the ACL config was never successfully reloaded")
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
