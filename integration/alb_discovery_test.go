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
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// shared helpers for the ALB autodiscovery integration scenarios
// ---------------------------------------------------------------------------

// discoveryHTTPClient bounds every request the discovery scenarios make,
// so a frozen endpoint (e.g. a paused container) fails an assertion
// quickly instead of hanging the test until the suite timeout
var discoveryHTTPClient = &http.Client{Timeout: 15 * time.Second}

// metricValue silently scrapes the named metric (with the given label
// fragment) from the metrics endpoint; returns the value and whether it
// was found
func metricValue(t *testing.T, metricsAddr, name, labelFragment string) (float64, bool) {
	t.Helper()
	resp, err := discoveryHTTPClient.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0, false
	}
	for line := range strings.Lines(string(b)) {
		if !strings.HasPrefix(line, name) {
			continue
		}
		if labelFragment != "" && !strings.Contains(line, labelFragment) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			continue
		}
		return v, true
	}
	return 0, false
}

// waitDiscoveredMembers polls the trickster_alb_discovery_members gauge for
// the named ALB until it reaches want, within the optional timeout
// (default 20s)
func waitDiscoveredMembers(t *testing.T, metricsAddr, albName string,
	want float64, timeout ...time.Duration,
) {
	t.Helper()
	limit := 20 * time.Second
	if len(timeout) > 0 {
		limit = timeout[0]
	}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		v, ok := metricValue(t, metricsAddr,
			"trickster_alb_discovery_members", `alb_name="`+albName+`"`)
		if !assert.True(collect, ok, "discovery members gauge not present") {
			return
		}
		assert.Equal(collect, want, v)
	}, limit, 100*time.Millisecond,
		"ALB %s never reached %v discovered members", albName, want)
}

// discoveryLeaf is a plain-HTTP upstream whose body identifies it, with a
// hit counter for traffic-apportionment assertions
type discoveryLeaf struct {
	name   string
	server *httptest.Server
	hits   atomic.Int64
}

func newDiscoveryLeaf(t *testing.T, name string) *discoveryLeaf {
	l := &discoveryLeaf{name: name}
	l.server = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			l.hits.Add(1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, name)
		}))
	t.Cleanup(l.server.Close)
	return l
}

func (l *discoveryLeaf) port() string {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(l.server.URL, "http://"))
	return port
}

// discoveryALBConfig renders a config with an rp template and a
// discovery-backed round-robin ALB. discoveryBlock is the YAML for the
// top-level discovery section entry; queryBlock the alb.discovery query.
func discoveryALBConfig(frontPort, metricsPort, mgmtPort int,
	discoveryBlock, queryBlock string,
) string {
	return fmt.Sprintf(`
listeners:
  default:
    port: %d
  metrics:
    port: %d
  mgmt:
    port: %d
mgmt:
  reload_drain_timeout: 250ms
logging:
  log_level: info
discovery:
%s
backends:
  member-template:
    provider: rp
    is_template: true
  disco-alb:
    provider: alb
    is_default: true
    alb:
      mechanism: rr
      discovery:
        discoverer_name: d1
        template_backend: member-template
        query:
%s
`, frontPort, metricsPort, mgmtPort, discoveryBlock, queryBlock)
}

// startDiscoveryTrickster writes the config and boots a trickster daemon,
// returning the config path for tests that reload
func startDiscoveryTrickster(t *testing.T, cfg string) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o644))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	return cfgPath
}

// getBody GETs the URL (with a bounded timeout) and returns status and body
func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := discoveryHTTPClient.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

// ---------------------------------------------------------------------------
// file provider: watched member-list live-edit (plan step 35)
// ---------------------------------------------------------------------------

// writeMembersFile atomically replaces the member-list file with entries
// for the provided leaves
func writeMembersFile(t *testing.T, path string, leaves ...*discoveryLeaf) {
	t.Helper()
	var b strings.Builder
	for _, l := range leaves {
		fmt.Fprintf(&b, "- name: %s\n  address: 127.0.0.1:%s\n", l.name, l.port())
	}
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(b.String()), 0o644))
	require.NoError(t, os.Rename(tmp, path))
}

func TestALBDiscoveryFileLiveEdit(t *testing.T) {
	const (
		frontPort   = 19510
		metricsPort = 19511
		mgmtPort    = 19512
	)
	leafA := newDiscoveryLeaf(t, "leafA")
	leafB := newDiscoveryLeaf(t, "leafB")
	leafC := newDiscoveryLeaf(t, "leafC")

	membersPath := filepath.Join(t.TempDir(), "members.yaml")
	writeMembersFile(t, membersPath, leafA, leafB)

	cfg := discoveryALBConfig(frontPort, metricsPort, mgmtPort,
		"  d1:\n    provider: file",
		"          path: "+membersPath)
	startDiscoveryTrickster(t, cfg)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)

	frontURL := fmt.Sprintf("http://127.0.0.1:%d/", frontPort)
	// round robin reaches both discovered members
	for range 10 {
		status, _ := getBody(t, frontURL)
		require.Equal(t, http.StatusOK, status)
	}
	require.Positive(t, leafA.hits.Load())
	require.Positive(t, leafB.hits.Load())

	// live-edit: drop A, add C; membership follows the file atomically
	writeMembersFile(t, membersPath, leafB, leafC)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 2)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		status, _ := getBody(t, frontURL)
		assert.Equal(collect, http.StatusOK, status)
		assert.Positive(collect, leafC.hits.Load(), "leafC not yet in rotation")
	}, 15*time.Second, 100*time.Millisecond)

	// A is drained out: after C is demonstrably in rotation, A stops
	// accumulating hits
	aHits := leafA.hits.Load()
	for range 10 {
		status, _ := getBody(t, frontURL)
		require.Equal(t, http.StatusOK, status)
	}
	require.Equal(t, aHits, leafA.hits.Load(),
		"removed member must no longer receive traffic")

	// scale to a single member
	writeMembersFile(t, membersPath, leafC)
	waitDiscoveredMembers(t, metricsAddr, "disco-alb", 1)
}
