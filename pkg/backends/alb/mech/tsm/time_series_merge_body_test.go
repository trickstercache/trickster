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
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

var mergeFn = merge.TimeseriesMergeFunc(nil)

func stubMergeHandler(marker string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc != nil {
			rsc.TS = newMarkerDataSet(marker)
			rsc.MergeFunc = mergeFn
			rsc.MergeRespondFunc = markerRespondFunc
		}
		w.Header().Set(headers.NameTricksterResult, "engine=none")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("ok"))
	})
}

func newMarkerDataSet(marker string) *dataset.DataSet {
	return &dataset.DataSet{
		Warnings: []string{marker},
		Results: dataset.Results{{
			SeriesList: dataset.SeriesList{{
				Header: dataset.SeriesHeader{Name: marker},
			}},
		}},
	}
}

func stubFailHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameTricksterResult, "engine=none")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("boom"))
	})
}

func markerRespondFunc(w http.ResponseWriter, _ *http.Request, accum *merge.Accumulator, statusCode int) {
	headers.StripMergeHeaders(w.Header())
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	ts := accum.GetTSData()
	ds, _ := ts.(*dataset.DataSet)
	w.WriteHeader(statusCode)
	if ds == nil {
		_, _ = w.Write([]byte("MERGED:nil"))
		return
	}
	names := make([]string, 0)
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			names = append(names, s.Header.Name)
		}
	}
	out := "MERGED:series=" + strings.Join(names, ",") +
		"|warnings=" + strings.Join(ds.Warnings, ",")
	_, _ = w.Write([]byte(out))
}

func newTestMergeRequest(t *testing.T) *http.Request {
	t.Helper()
	r := albpool.NewParentGET(t)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.IsMergeMember = true
	return request.SetResources(r, rsc)
}

func TestHandleResponseMergeBody(t *testing.T) {
	logger.SetLogger(testLogger)

	t.Run("two_members_body_carries_both_markers", func(t *testing.T) {
		p, _, _ := albpool.NewHealthy([]http.Handler{
			stubMergeHandler("alpha", http.StatusOK),
			stubMergeHandler("beta", http.StatusOK),
		})
		defer p.Stop()
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		p.RefreshHealthy()

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newTestMergeRequest(t))

		if w.Code != http.StatusOK {
			t.Fatalf("status: want %d got %d", http.StatusOK, w.Code)
		}
		body := w.Body.String()
		if !strings.HasPrefix(body, "MERGED:series=") {
			t.Fatalf("body: want MERGED:series= prefix, got %q", body)
		}
		seriesPart := body[len("MERGED:series="):strings.Index(body, "|")]
		if !strings.Contains(seriesPart, "alpha") || !strings.Contains(seriesPart, "beta") {
			t.Fatalf("series: want both markers (alpha, beta), got %q", seriesPart)
		}
		if got := w.Header().Get(headers.NameTricksterResult); got == "" {
			t.Fatalf("%s header: want non-empty, got empty", headers.NameTricksterResult)
		}
	})

	t.Run("partial_failure_marks_phit_and_warns_in_body", func(t *testing.T) {
		p, _, _ := albpool.NewHealthy([]http.Handler{
			stubMergeHandler("alpha", http.StatusOK),
			stubFailHandler(http.StatusInternalServerError),
		})
		defer p.Stop()
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		p.RefreshHealthy()

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newTestMergeRequest(t))

		body := w.Body.String()
		if !strings.Contains(body, "alpha") {
			t.Fatalf("body: want successful member marker (alpha), got %q", body)
		}
		if !strings.Contains(body, "tsm partial failure") {
			t.Fatalf("body: want partial-failure warning, got %q", body)
		}
		if status := w.Header().Get(headers.NameTricksterResult); !strings.Contains(status, "phit") {
			t.Fatalf("%s header: want phit marker, got %q", headers.NameTricksterResult, status)
		}
	})

	t.Run("all_members_without_merge_contribution_returns_502", func(t *testing.T) {
		p, _, _ := albpool.NewHealthy([]http.Handler{
			stubFailHandler(http.StatusInternalServerError),
			stubFailHandler(http.StatusBadGateway),
		})
		defer p.Stop()
		h := &handler{mergePaths: []string{"/"}}
		h.SetPool(p)
		p.RefreshHealthy()

		w := httptest.NewRecorder()
		h.ServeHTTP(w, newTestMergeRequest(t))

		if w.Code != http.StatusBadGateway {
			t.Fatalf("status: want %d got %d body=%q", http.StatusBadGateway, w.Code, w.Body.String())
		}
	})
}
