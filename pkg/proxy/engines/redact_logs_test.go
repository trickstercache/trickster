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

package engines

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/redact"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

const (
	testLogPassword = "url-super-secret"
	testLogQuery    = "user=default&password=" + testLogPassword
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs routes the global logger to a buffer at debug level for the test's duration.
func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	logger.SetLogger(logging.StreamLogger(buf, level.Debug))
	t.Cleanup(func() { logger.SetLogger(testLogger) })
	return buf
}

func requireRedacted(t *testing.T, buf *lockedBuffer, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(buf.String(), message) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	out := buf.String()
	if !strings.Contains(out, message) {
		t.Fatalf("no %q log line:\n%s", message, out)
	}
	if strings.Contains(out, testLogPassword) {
		t.Fatalf("a log line exposed the URL password:\n%s", out)
	}
	if !strings.Contains(out, "password="+redact.Value) {
		t.Fatalf("no redacted password in the log:\n%s", out)
	}
}

func TestErrorLogsRedactURLCredentials(t *testing.T) {
	const badUpstream = "http://127.0.0.1:64389"
	conf, err := config.Load([]string{"-origin-url", badUpstream, "-provider", testResponseBody})
	if err != nil {
		t.Fatal(err)
	}
	o := conf.Backends["default"]
	tr := &http.Transport{}
	o.HTTPClient = &http.Client{Transport: tr}
	t.Cleanup(tr.CloseIdleConnections)
	r := httptest.NewRequest(http.MethodGet, badUpstream+"/?"+testLogQuery, nil)
	r = r.WithContext(tc.WithResources(r.Context(),
		request.NewResources(o, &po.Options{Path: "/"}, nil, nil, nil, tu.NewTestTracer())))
	buf := captureLogs(t) // after NewTestTracer, which resets the global logger
	DoProxy(httptest.NewRecorder(), r, true)
	requireRedacted(t, buf, "error reaching upstream origin")
	requireRedacted(t, buf, "error downloading url")
}

func TestUpstreamDebugLogRedactsURLCredentials(t *testing.T) {
	ts, w, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestHarness(ts, r)
	step := 300 * time.Second
	end := time.Now().Add(-12 * time.Hour)
	r.URL.Path = "/prometheus/api/v1/query_range"
	r.URL.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s&%s", int(step.Seconds()),
		end.Add(-time.Hour).Unix(), end.Unix(), queryReturnsOKNoLatency, testLogQuery)
	buf := captureLogs(t)
	rsc.BackendClient.(*TestClient).QueryRangeHandler(w, r)
	requireRedacted(t, buf, "upstream request")
}
