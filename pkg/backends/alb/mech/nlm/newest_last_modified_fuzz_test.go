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

package nlm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// FuzzNLMLastModifiedSelection drives the NLM winner-selection path with
// arbitrary upstream-supplied Last-Modified header values. Invariants:
//   - ServeHTTP never panics.
//   - The selection is deterministic: two back-to-back runs with identical
//     inputs return the same body+status.
//   - A slot whose Last-Modified is unparsable by http.ParseTime never wins
//     via the LM-comparison path. If any slot has a parseable LM, the winner
//     must be one of the parseable slots. If no slot is parseable, fallback
//     takes the first 2xx slot.
func FuzzNLMLastModifiedSelection(f *testing.F) {
	now := time.Now().UTC().Truncate(time.Second)
	f.Add(
		[]byte(now.Format(http.TimeFormat)),
		[]byte(now.Add(-time.Hour).Format(http.TimeFormat)),
		[]byte(now.Add(-24*time.Hour).Format(http.TimeFormat)),
	)
	f.Add([]byte("Sun, 06 Nov 1994 08:49:37 GMT"), []byte("Sunday, 06-Nov-94 08:00:00 GMT"), []byte("Sun Nov  6 08:49:37 1994"))
	f.Add([]byte(""), []byte("not a date"), []byte("Sun, 06 Nov 1994 08:49:37 GMT"))
	f.Add([]byte("\x00\x00\x00"), []byte("0000-00-00 00:00:00"), []byte(""))
	f.Add([]byte(strings.Repeat("A", 4096)), []byte("Sun, 06 Nov 1994 08:49:37 GMT"), []byte("garbage\twith\ttabs"))
	f.Add([]byte("Sun, 06 Nov 9999 08:49:37 GMT"), []byte("Sun, 06 Nov 1994 08:49:37 GMT"), []byte(""))

	f.Fuzz(func(t *testing.T, a, b, c []byte) {
		inputs := [3][]byte{a, b, c}
		// Reject header values containing CR/LF; setting these via the
		// stdlib's response.Write path would be a transport-level error
		// elsewhere. The fuzz target is the parser/selector, not header
		// validation, so skip these inputs.
		for _, in := range inputs {
			if hasCRLF(in) {
				t.Skip("CR/LF in header value")
			}
		}

		run := func() (int, string, int) {
			hs := make([]http.Handler, len(inputs))
			for i, in := range inputs {
				lm := string(in)
				body := fmt.Sprintf("slot-%d", i)
				hs[i] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					if lm != "" {
						w.Header().Set(headers.NameLastModified, lm)
					}
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(body))
				})
			}

			p, _, st := albpool.New(-1, hs)
			defer p.Stop()
			for _, s := range st {
				s.Set(0)
			}
			p.RefreshHealthy()

			h := &handler{}
			h.SetPool(p)
			w := httptest.NewRecorder()
			r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
			h.ServeHTTP(w, r)
			return w.Code, w.Body.String(), parseSlotIdx(w.Body.String())
		}

		code1, body1, winner1 := run()
		code2, body2, _ := run()

		if code1 != code2 || body1 != body2 {
			t.Fatalf("non-deterministic selection: run1=(%d,%q) run2=(%d,%q) inputs=%q",
				code1, body1, code2, body2, inputs)
		}
		if code1 != http.StatusOK {
			t.Fatalf("expected 200 from healthy slots, got %d body=%q inputs=%q",
				code1, body1, inputs)
		}
		if winner1 < 0 || winner1 > 2 {
			t.Fatalf("body %q did not match any slot marker; inputs=%q", body1, inputs)
		}

		parseable := [3]bool{}
		anyParseable := false
		for i, in := range inputs {
			if len(in) == 0 {
				continue
			}
			if _, err := http.ParseTime(string(in)); err == nil {
				parseable[i] = true
				anyParseable = true
			}
		}

		if anyParseable && !parseable[winner1] {
			t.Errorf("winner slot %d has unparsable LM %q but a parseable slot existed; inputs=%q",
				winner1, string(inputs[winner1]), inputs)
		}
	})
}

func hasCRLF(b []byte) bool {
	for _, c := range b {
		if c == '\r' || c == '\n' {
			return true
		}
	}
	return false
}

// parseSlotIdx returns the slot index encoded in a body produced by the
// fuzz handlers, or -1 if the body does not match the expected marker.
func parseSlotIdx(body string) int {
	const prefix = "slot-"
	if !strings.HasPrefix(body, prefix) {
		return -1
	}
	switch body[len(prefix):] {
	case "0":
		return 0
	case "1":
		return 1
	case "2":
		return 2
	}
	return -1
}
