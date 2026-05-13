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

func TestPickWinnerPrefersFirst2xx(t *testing.T) {
	called := map[string]bool{}
	mk := func(name string) merge.RespondFunc {
		return func(_ http.ResponseWriter, _ *http.Request, _ *merge.Accumulator, _ int) {
			called[name] = true
		}
	}
	results := []gatherResult{
		{statusCode: 500, mergeFunc: mk("err0"), header: http.Header{"X-Slot": []string{"0"}}},
		{statusCode: 200, mergeFunc: mk("ok1"), header: http.Header{"X-Slot": []string{"1"}}},
		{statusCode: 200, mergeFunc: mk("ok2"), header: http.Header{"X-Slot": []string{"2"}}},
	}
	mrf, hdrs := pickWinner(results)
	if mrf == nil {
		t.Fatal("expected a winner")
	}
	mrf(nil, nil, nil, 0)
	if !called["ok1"] {
		t.Errorf("expected first 2xx winner, got %v", called)
	}
	if hdrs.Get("X-Slot") != "1" {
		t.Errorf("expected slot 1 headers, got %q", hdrs.Get("X-Slot"))
	}
}

func TestPickWinnerFallsBackToFirstNon2xx(t *testing.T) {
	called := map[string]bool{}
	mk := func(name string) merge.RespondFunc {
		return func(_ http.ResponseWriter, _ *http.Request, _ *merge.Accumulator, _ int) {
			called[name] = true
		}
	}
	results := []gatherResult{
		{statusCode: 500, mergeFunc: mk("err0"), header: http.Header{"X-Slot": []string{"0"}}},
		{statusCode: 502, mergeFunc: mk("err1"), header: http.Header{"X-Slot": []string{"1"}}},
	}
	mrf, hdrs := pickWinner(results)
	if mrf == nil {
		t.Fatal("expected fallback winner")
	}
	mrf(nil, nil, nil, 0)
	if !called["err0"] {
		t.Errorf("expected first non-2xx winner, got %v", called)
	}
	if hdrs.Get("X-Slot") != "0" {
		t.Errorf("expected slot 0 headers, got %q", hdrs.Get("X-Slot"))
	}
}

func TestPickWinnerNoneReturnsNil(t *testing.T) {
	results := []gatherResult{{statusCode: 0, mergeFunc: nil}, {failed: true}}
	mrf, hdrs := pickWinner(results)
	if mrf != nil || hdrs != nil {
		t.Errorf("expected nil winner and headers, got %v %v", mrf, hdrs)
	}
}

func TestAggregateStatusAllSuccess(t *testing.T) {
	results := []gatherResult{
		{statusCode: 200, header: http.Header{headers.NameTricksterResult: []string{"engine=A"}}},
		{statusCode: 206, header: http.Header{headers.NameTricksterResult: []string{"engine=B"}}},
	}
	code, _, has2xx, hasNon2xx := aggregateStatus(results)
	if code != 200 {
		t.Errorf("expected min 2xx (200), got %d", code)
	}
	if !has2xx || hasNon2xx {
		t.Errorf("flags: has2xx=%v hasNon2xx=%v", has2xx, hasNon2xx)
	}
}

func TestAggregateStatusMixedPrefers200(t *testing.T) {
	results := []gatherResult{
		{statusCode: 500},
		{statusCode: 200},
		{statusCode: 502},
	}
	code, _, has2xx, hasNon2xx := aggregateStatus(results)
	if code != 200 {
		t.Errorf("mixed-success should still pick 200, got %d", code)
	}
	if !has2xx || !hasNon2xx {
		t.Errorf("expected both flags, got has2xx=%v hasNon2xx=%v", has2xx, hasNon2xx)
	}
}

func TestAggregateStatusAllErrorPicksMax(t *testing.T) {
	// Before this PR the aggregator returned the min, surfacing 400 and
	// hiding the more severe 502s. The new behavior surfaces 502.
	results := []gatherResult{
		{statusCode: 400},
		{statusCode: 502},
		{statusCode: 502},
	}
	code, _, has2xx, hasNon2xx := aggregateStatus(results)
	if code != 502 {
		t.Errorf("all-error should pick max (502), got %d", code)
	}
	if has2xx || !hasNon2xx {
		t.Errorf("flags: has2xx=%v hasNon2xx=%v", has2xx, hasNon2xx)
	}
}

func TestAggregateStatusEmpty(t *testing.T) {
	code, sh, has2xx, hasNon2xx := aggregateStatus(nil)
	if code != 0 || sh != "" || has2xx || hasNon2xx {
		t.Errorf("zero state: code=%d sh=%q has2xx=%v hasNon2xx=%v", code, sh, has2xx, hasNon2xx)
	}
}

func TestMergeMultiValuedHeadersPreservesSetCookie(t *testing.T) {
	results := []gatherResult{
		{header: http.Header{headers.NameSetCookie: []string{"a=1", "b=2"}}},
		{header: http.Header{headers.NameSetCookie: []string{"c=3"}}},
	}
	winner := http.Header{headers.NameSetCookie: []string{"winner=ignored"}}
	dst := http.Header{}
	mergeMultiValuedHeaders(dst, results, winner)
	got := dst.Values(headers.NameSetCookie)
	if len(got) != 3 {
		t.Fatalf("expected 3 Set-Cookie values, got %d: %v", len(got), got)
	}
	if winner.Get(headers.NameSetCookie) != "" {
		t.Errorf("winner should have Set-Cookie deleted to avoid double-merge, still has %q",
			winner.Get(headers.NameSetCookie))
	}
}

func TestMergeMultiValuedHeadersSkipsNilHeader(t *testing.T) {
	results := []gatherResult{
		{header: nil},
		{header: http.Header{headers.NameSetCookie: []string{"only=1"}}},
	}
	dst := http.Header{}
	mergeMultiValuedHeaders(dst, results, nil)
	if got := dst.Values(headers.NameSetCookie); len(got) != 1 || got[0] != "only=1" {
		t.Errorf("expected single Set-Cookie, got %v", got)
	}
}
