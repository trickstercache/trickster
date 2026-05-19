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

package tsm

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
)

func TestPickWinner(t *testing.T) {
	type slot struct {
		name       string
		statusCode int
		failed     bool
		hdrSlot    string
	}
	cases := []struct {
		name        string
		slots       []slot
		wantNil     bool
		wantCalled  string // name of mergeFunc expected to fire
		wantHdrSlot string // expected X-Slot on returned headers
	}{
		{
			name: "prefers first 2xx over earlier 5xx",
			slots: []slot{
				{name: "err0", statusCode: 500, hdrSlot: "0"},
				{name: "ok1", statusCode: 200, hdrSlot: "1"},
				{name: "ok2", statusCode: 200, hdrSlot: "2"},
			},
			wantCalled:  "ok1",
			wantHdrSlot: "1",
		},
		{
			name: "falls back to first non-2xx when no 2xx present",
			slots: []slot{
				{name: "err0", statusCode: 500, hdrSlot: "0"},
				{name: "err1", statusCode: 502, hdrSlot: "1"},
			},
			wantCalled:  "err0",
			wantHdrSlot: "0",
		},
		{
			name: "no candidates returns nil winner and nil headers",
			slots: []slot{
				{statusCode: 0},
				{failed: true},
			},
			wantNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := map[string]bool{}
			mk := func(name string) merge.RespondFunc {
				if name == "" {
					return nil
				}
				return func(_ http.ResponseWriter, _ *http.Request, _ *merge.Accumulator, _ int) {
					called[name] = true
				}
			}
			results := make([]gatherResult, len(tc.slots))
			for i, s := range tc.slots {
				gr := gatherResult{
					statusCode: s.statusCode,
					mergeFunc:  mk(s.name),
					failed:     s.failed,
				}
				if s.hdrSlot != "" {
					gr.header = http.Header{"X-Slot": []string{s.hdrSlot}}
				}
				results[i] = gr
			}

			mrf, hdrs := pickWinner(results)
			if tc.wantNil {
				if mrf != nil || hdrs != nil {
					t.Errorf("case=%q slots=%+v: expected nil winner and headers, got mrf=%v hdrs=%v",
						tc.name, tc.slots, mrf, hdrs)
				}
				return
			}
			if mrf == nil {
				t.Fatalf("case=%q slots=%+v: expected a winner, got nil", tc.name, tc.slots)
			}
			mrf(nil, nil, nil, 0)
			if !called[tc.wantCalled] {
				t.Errorf("case=%q slots=%+v: expected %q to be called, got called=%v",
					tc.name, tc.slots, tc.wantCalled, called)
			}
			if got := hdrs.Get("X-Slot"); got != tc.wantHdrSlot {
				t.Errorf("case=%q slots=%+v: expected X-Slot=%q, got %q",
					tc.name, tc.slots, tc.wantHdrSlot, got)
			}
		})
	}
}

func TestAggregateStatus(t *testing.T) {
	cases := []struct {
		name         string
		results      []gatherResult
		wantCode     int
		checkSH      bool // original test asserted on statusHeader
		wantSH       string
		wantHas2xx   bool
		wantHasNon2x bool
	}{
		{
			name: "all 2xx picks min 2xx",
			results: []gatherResult{
				{statusCode: 200, header: http.Header{headers.NameTricksterResult: []string{"engine=A"}}},
				{statusCode: 206, header: http.Header{headers.NameTricksterResult: []string{"engine=B"}}},
			},
			wantCode:   200,
			wantHas2xx: true,
		},
		{
			name: "mixed success and error still picks 200",
			results: []gatherResult{
				{statusCode: 500},
				{statusCode: 200},
				{statusCode: 502},
			},
			wantCode:     200,
			wantHas2xx:   true,
			wantHasNon2x: true,
		},
		{
			// Before the fix this returned the min (400), hiding more severe 502s.
			name: "all errors surfaces max status",
			results: []gatherResult{
				{statusCode: 400},
				{statusCode: 502},
				{statusCode: 502},
			},
			wantCode:     502,
			wantHasNon2x: true,
		},
		{
			name:    "empty input returns zero state",
			results: nil,
			checkSH: true,
			wantSH:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, sh, has2xx, hasNon2xx := aggregateStatus(tc.results)
			if code != tc.wantCode {
				t.Errorf("case=%q input=%+v: code got %d want %d", tc.name, tc.results, code, tc.wantCode)
			}
			if tc.checkSH && sh != tc.wantSH {
				t.Errorf("case=%q input=%+v: statusHeader got %q want %q", tc.name, tc.results, sh, tc.wantSH)
			}
			if has2xx != tc.wantHas2xx {
				t.Errorf("case=%q input=%+v: has2xx got %v want %v", tc.name, tc.results, has2xx, tc.wantHas2xx)
			}
			if hasNon2xx != tc.wantHasNon2x {
				t.Errorf("case=%q input=%+v: hasNon2xx got %v want %v", tc.name, tc.results, hasNon2xx, tc.wantHasNon2x)
			}
		})
	}
}

func TestMergeMultiValuedHeaders(t *testing.T) {
	cases := []struct {
		name              string
		results           []gatherResult
		winner            http.Header
		wantSetCookies    []string
		wantWinnerCleared bool
	}{
		{
			name: "forwards winner Set-Cookie and clears winner copy",
			results: []gatherResult{
				{header: http.Header{headers.NameSetCookie: []string{"loser1=a"}}},
				{header: http.Header{headers.NameSetCookie: []string{"loser2=b"}}},
			},
			winner:            http.Header{headers.NameSetCookie: []string{"winner=v1", "winner=v2"}},
			wantSetCookies:    []string{"winner=v1", "winner=v2"},
			wantWinnerCleared: true,
		},
		{
			name: "drops all Set-Cookies when winner has none",
			results: []gatherResult{
				{header: http.Header{headers.NameSetCookie: []string{"loser=1"}}},
			},
			winner:         http.Header{},
			wantSetCookies: nil,
		},
		{
			name:           "nil winner drops all",
			results:        []gatherResult{{header: http.Header{headers.NameSetCookie: []string{"loser=1"}}}},
			winner:         nil,
			wantSetCookies: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := http.Header{}
			mergeMultiValuedHeaders(dst, tc.results, tc.winner)
			got := dst.Values(headers.NameSetCookie)
			if len(got) != len(tc.wantSetCookies) {
				t.Fatalf("case=%q results=%+v: Set-Cookie count got %d (%v) want %d (%v)",
					tc.name, tc.results, len(got), got, len(tc.wantSetCookies), tc.wantSetCookies)
			}
			for i, v := range tc.wantSetCookies {
				if got[i] != v {
					t.Errorf("case=%q results=%+v: Set-Cookie[%d] got %q want %q",
						tc.name, tc.results, i, got[i], v)
				}
			}
			if tc.wantWinnerCleared && tc.winner.Get(headers.NameSetCookie) != "" {
				t.Errorf("case=%q: winner Set-Cookie should be cleared to avoid double-merge, still has %q",
					tc.name, tc.winner.Get(headers.NameSetCookie))
			}
		})
	}
}
