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

// Package promstub centralizes the small Prometheus-compatible stub pieces
// reused across the ALB integration tests: the /api/v1/status/buildinfo
// response (used by every test that boots trickster with a Prometheus backend
// healthcheck) and the trickster YAML preamble + per-backend stanza that
// otherwise gets duplicated byte-for-byte across writeMatrixConfig,
// writeChaosConfig, and the disconnect test's inline builder.
package promstub

import (
	"fmt"
	"net/http"
	"strings"
)

// BuildInfoPath is the canonical Prometheus buildinfo URL trickster's
// healthcheck probes by default.
const BuildInfoPath = "/api/v1/status/buildinfo"

// buildInfoBody is the minimal Prometheus buildinfo response trickster's
// healthcheck accepts.
const buildInfoBody = `{"status":"success","data":{"version":"2.0"}}`

// WriteBuildInfo writes the canonical Prometheus buildinfo response to w.
// Use when a handler dispatches on r.URL.Path and needs to short-circuit
// the buildinfo probe before falling through to the test-specific logic.
func WriteBuildInfo(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(buildInfoBody))
}

// BuildInfoHandler returns an http.Handler that always responds with the
// canonical Prometheus buildinfo body. Use when the calling test wires
// handlers via http.ServeMux.
func BuildInfoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteBuildInfo(w)
	})
}

// Preamble emits the trickster YAML frontend/metrics/mgmt/logging/caches
// stanza common to every ALB integration test. Callers append a backends
// section and an alb stanza below.
func Preamble(frontPort, metricsPort, mgmtPort int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "frontend:\n  listen_port: %d\n", frontPort)
	fmt.Fprintf(&sb, "metrics:\n  listen_port: %d\n", metricsPort)
	fmt.Fprintf(&sb, "mgmt:\n  listen_port: %d\n", mgmtPort)
	sb.WriteString("logging:\n  log_level: error\n")
	sb.WriteString("caches:\n  mem1:\n    provider: memory\n")
	return sb.String()
}

// BackendStanza emits one Prometheus-backend block with the canonical
// buildinfo healthcheck wired to BuildInfoPath. Caller is responsible for
// emitting the parent "backends:\n" header before the first stanza.
func BackendStanza(name, originURL string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "  %s:\n", name)
	sb.WriteString("    provider: prometheus\n")
	fmt.Fprintf(&sb, "    origin_url: %s\n", originURL)
	sb.WriteString("    cache_name: mem1\n")
	sb.WriteString("    healthcheck:\n")
	fmt.Fprintf(&sb, "      path: %s\n", BuildInfoPath)
	sb.WriteString("      query: \"\"\n")
	sb.WriteString("      interval: 100ms\n")
	sb.WriteString("      timeout: 500ms\n")
	sb.WriteString("      failure_threshold: 1\n")
	sb.WriteString("      recovery_threshold: 1\n")
	return sb.String()
}
