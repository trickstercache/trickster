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

package listener

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

const (
	limitsTestRequest = "GET / HTTP/1.1\r\nHost: limits.test\r\n\r\n"
	limitsShortWait   = 100 * time.Millisecond
	limitsReadyWait   = 5 * time.Second
)

func startLimitedListener(t *testing.T, name string, limits ServerLimits) *Listener {
	t.Helper()
	logger.SetLogger(logging.NoopLogger())
	lg := NewGroup()
	t.Cleanup(func() { _ = lg.DrainAndClose(name, time.Second) })
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	go func() { _ = lg.StartListener(name, "127.0.0.1", 0, 0, nil, ok, nil, nil, limits, nil) }()
	deadline := time.Now().Add(limitsReadyWait)
	for time.Now().Before(deadline) {
		if l := lg.Get(name); l != nil && l.State() == StateReady {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener %s did not become ready", name)
	return nil
}

// roundTrip sends one keep-alive request on conn and returns the response status
func roundTrip(t *testing.T, conn net.Conn, br *bufio.Reader) int {
	t.Helper()
	if _, err := io.WriteString(conn, limitsTestRequest); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestServerLimitsIdleTimeout(t *testing.T) {
	l := startLimitedListener(t, "idle-limit", ServerLimits{IdleTimeout: limitsShortWait})
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if code := roundTrip(t, conn, br); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	// the server closes the idle connection, so the read ends long before the client's own deadline
	_ = conn.SetReadDeadline(time.Now().Add(limitsReadyWait))
	start := time.Now()
	if _, err := br.ReadByte(); err != io.EOF {
		t.Fatalf("expected EOF from an idle connection, got %v", err)
	}
	if elapsed := time.Since(start); elapsed >= limitsReadyWait {
		t.Errorf("idle connection held for %v", elapsed)
	}
}

func TestServerLimitsNoIdleTimeout(t *testing.T) {
	// a read timeout must not close a connection between requests when idle_timeout disables that
	l := startLimitedListener(t, "no-idle-limit",
		ServerLimits{ReadTimeout: limitsShortWait, IdleTimeout: NoIdleTimeout})
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	if code := roundTrip(t, conn, br); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	time.Sleep(3 * limitsShortWait)
	if code := roundTrip(t, conn, br); code != http.StatusOK {
		t.Fatalf("second request status %d", code)
	}
}

func TestServerLimitsMaxHeaderBytes(t *testing.T) {
	const maxHeader = 1024
	l := startLimitedListener(t, "header-limit", ServerLimits{MaxHeaderBytes: maxHeader})
	req, err := http.NewRequest(http.MethodGet, "http://"+l.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// net/http allows 4 KiB of slack beyond the configured limit, so exceed both
	req.Header.Set("X-Large", strings.Repeat("a", 8*maxHeader))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("status %d, want %d", resp.StatusCode, http.StatusRequestHeaderFieldsTooLarge)
	}
}
