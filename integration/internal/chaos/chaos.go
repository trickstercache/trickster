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

// Package chaos provides a polymorphic stub-upstream library for ALB and
// proxy integration tests. The handlers it exposes deliberately misbehave
// (truncate bodies, panic, return invalid status with a parseable
// Last-Modified, return warnings, sleep) so tests can exercise the failure
// modes the happy-path stubs in the rest of the suite never produce.
package chaos

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// BehaviorOK returns a 200 with the given JSON body and a JSON content-type.
func BehaviorOK(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

// BehaviorStatus returns the given status code with an empty body.
func BehaviorStatus(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}
}

// BehaviorTruncateStaleCL returns a 200 with Content-Length=staleLen but
// writes only actualLen bytes (zero-filled). Surfaces NLM/FR truncation
// handling: callers that trust Content-Length will over-read or hang.
func BehaviorTruncateStaleCL(staleLen, actualLen int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(staleLen))
		w.WriteHeader(http.StatusOK)
		if actualLen > 0 {
			buf := make([]byte, actualLen)
			_, _ = w.Write(buf)
		}
	}
}

// BehaviorPanic panics inside the handler. Surfaces panic recovery in
// fanout per-shard goroutines and health probe loops.
func BehaviorPanic() http.HandlerFunc {
	return func(_ http.ResponseWriter, _ *http.Request) {
		panic("chaos: BehaviorPanic")
	}
}

// Behavior5xxWithLM returns a non-2xx status with a parseable Last-Modified
// header. Surfaces NLM filter bugs where invalid status is paired with a
// valid LM (a slot that should be excluded by status may still be picked
// up by LM-only comparisons).
func Behavior5xxWithLM(code int, lm time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Last-Modified", lm.UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"status":"error","errorType":"internal","error":"chaos"}`))
	}
}

// BehaviorReturnsWarnings returns a 200 with a Prometheus-shaped response
// that includes a top-level warnings array. Surfaces Warnings/Status drop
// on merge in the TSM and NLM mechanisms.
func BehaviorReturnsWarnings(body string, warnings ...string) http.HandlerFunc {
	if warnings == nil {
		warnings = []string{}
	}
	wbuf, err := json.Marshal(warnings)
	if err != nil {
		// json.Marshal of []string never errs in practice; fall back to
		// the literal empty array so handlers never produce invalid JSON.
		wbuf = []byte(`[]`)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Inject "warnings": [...] before the closing brace. If the body
		// isn't a JSON object, fall back to a synthetic envelope.
		if len(body) > 1 && body[len(body)-1] == '}' {
			fmt.Fprintf(w, `%s,"warnings":%s}`, body[:len(body)-1], string(wbuf))
			return
		}
		fmt.Fprintf(w, `{"status":"success","data":%s,"warnings":%s}`, body, string(wbuf))
	}
}

// BehaviorSlowProbe sleeps for d before responding 200 with an empty
// success envelope. Honors request-context cancellation so the test
// process doesn't accumulate parked goroutines after the client gives up.
func BehaviorSlowProbe(d time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
	}
}
