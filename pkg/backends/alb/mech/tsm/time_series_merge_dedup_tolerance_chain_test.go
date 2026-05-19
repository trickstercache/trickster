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
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func tolerantStubHandler(epochNs int64, value, marker string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = &dataset.DataSet{
				Results: dataset.Results{{
					SeriesList: dataset.SeriesList{{
						Header: dataset.SeriesHeader{Name: "rps", Tags: dataset.Tags{}},
						Points: dataset.Points{{
							Epoch:  epoch.Epoch(epochNs),
							Size:   32,
							Values: []any{value},
						}},
					}},
				}},
			}
			rsc.MergeFunc = merge.TimeseriesMergeFuncTolerant(nil, rsc.TSDedupToleranceNanos)
			rsc.MergeRespondFunc = tolerantRespondFunc(marker)
		}
		w.Header().Set(headers.NameTricksterResult, "engine=none")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func tolerantRespondFunc(_ string) merge.RespondFunc {
	return func(w http.ResponseWriter, _ *http.Request, accum *merge.Accumulator, statusCode int) {
		headers.StripMergeHeaders(w.Header())
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		ds, _ := accum.GetTSData().(*dataset.DataSet)
		w.WriteHeader(statusCode)
		if ds == nil || len(ds.Results) == 0 || len(ds.Results[0].SeriesList) == 0 {
			_, _ = w.Write([]byte("MERGED:empty"))
			return
		}
		var sb strings.Builder
		sb.WriteString("MERGED:")
		for _, p := range ds.Results[0].SeriesList[0].Points {
			sb.WriteString(strconv.FormatInt(int64(p.Epoch), 10))
			sb.WriteByte('=')
			if len(p.Values) > 0 {
				if s, ok := p.Values[0].(string); ok {
					sb.WriteString(s)
				}
			}
			sb.WriteByte(';')
		}
		_, _ = w.Write([]byte(sb.String()))
	}
}

func newTolerantMergeRequest(t *testing.T) *http.Request {
	t.Helper()
	r := albpool.NewParentGET(t)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	return request.SetResources(r, rsc)
}

func TestTSMServeStandardDedupToleranceChain(t *testing.T) {
	logger.SetLogger(testLogger)

	t.Run("sub-tolerance epochs from two members collapse to one point", func(t *testing.T) {
		tolMs := 5
		p, _, _ := albpool.NewHealthy([]http.Handler{
			tolerantStubHandler(1000, "1.0", "alpha"),
			tolerantStubHandler(1003, "2.0", "beta"),
		})
		defer p.Stop()
		h := &handler{
			mergePaths: []string{"/"},
			tsmOptions: options.TimeSeriesMergeOptions{DedupToleranceMs: &tolMs},
		}
		h.SetPool(p)
		albpool.WaitHealthy(t, p, 2)

		if got := h.dedupToleranceNanos(); got != 5_000_000 {
			t.Fatalf("dedupToleranceNanos: want 5_000_000, got %d", got)
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newTolerantMergeRequest(t))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want %d got %d (body=%q)", http.StatusOK, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.HasPrefix(body, "MERGED:") {
			t.Fatalf("body: want MERGED: prefix, got %q", body)
		}
		points := strings.Split(strings.TrimPrefix(body, "MERGED:"), ";")
		var nonEmpty []string
		for _, p := range points {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		if len(nonEmpty) != 1 {
			t.Fatalf("expected sub-tolerance epochs to collapse to 1 point, got %d (%q)", len(nonEmpty), body)
		}
		got := nonEmpty[0]
		if !strings.HasPrefix(got, "1000=") {
			t.Errorf("expected first-seen point at epoch 1000, got %q", got)
		}
		if !strings.HasSuffix(got, "=1.0") {
			t.Errorf("expected first-seen value 1.0, got %q", got)
		}
	})

	t.Run("tolerance zero keeps both points (baseline)", func(t *testing.T) {
		// Baseline: with DedupToleranceMs unset, near-duplicate epochs from
		// independent shards are preserved (legacy exact-epoch dedup). This
		// pins the chain: only the tolerance opt-in collapses them.
		p, _, _ := albpool.NewHealthy([]http.Handler{
			tolerantStubHandler(1000, "1.0", "alpha"),
			tolerantStubHandler(1003, "2.0", "beta"),
		})
		defer p.Stop()
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		albpool.WaitHealthy(t, p, 2)

		if got := h.dedupToleranceNanos(); got != 0 {
			t.Fatalf("dedupToleranceNanos: want 0 (disabled), got %d", got)
		}

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newTolerantMergeRequest(t))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want %d got %d", http.StatusOK, w.Code)
		}
		points := strings.Split(strings.TrimPrefix(w.Body.String(), "MERGED:"), ";")
		var nonEmpty []string
		for _, p := range points {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		if len(nonEmpty) != 2 {
			t.Fatalf("baseline: expected both points preserved without tolerance, got %d (%v)", len(nonEmpty), nonEmpty)
		}
	})
}
