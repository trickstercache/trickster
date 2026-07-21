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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	prommodel "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	responsemerge "github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	tsmerge "github.com/trickstercache/trickster/v2/pkg/timeseries/merge"
)

func scalarMember(body string, instant bool, trq *timeseries.TimeRangeQuery) http.Handler {
	return scalarMemberStatus(body, instant, trq, http.StatusOK)
}

func scalarMemberStatus(body string, instant bool, trq *timeseries.TimeRangeQuery,
	status int,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		m := prommodel.NewModeler()
		rsc.TimeRangeQuery = trq.Clone()
		rsc.TSUnmarshaler = m.WireUnmarshaler
		if instant {
			rsc.MergeFunc = prommodel.MergeAndWriteVectorMergeFunc(m.WireUnmarshaler)
			rsc.BatchMergeFunc = prommodel.MergeAndWriteVectorBatchMergeFunc()
			rsc.MergeRespondFunc = prommodel.MergeAndWriteVectorRespondFunc(m.WireMarshalWriter)
		} else {
			rsc.MergeFunc = responsemerge.TimeseriesMergeFuncWithStrategy(
				m.WireUnmarshaler, int(tsmerge.StrategyScalar))
			rsc.BatchMergeFunc = responsemerge.TimeseriesBatchMergeFuncWithStrategy(
				int(tsmerge.StrategyScalar))
			rsc.MergeRespondFunc = responsemerge.TimeseriesRespondFuncWithStrategy(
				m.WireMarshalWriter, nil, int(tsmerge.StrategyScalar))
		}
		w.Header().Set(headers.NameContentType, "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	})
}

func TestServeStandardScalarInstantIgnoresErrorEnvelope(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Statement: "(scalar(count(up)))"}
	p, _, _ := albpool.NewHealthy([]http.Handler{
		scalarMemberStatus(`{"status":"error","errorType":"bad_data","error":"boom"}`,
			true, trq, http.StatusInternalServerError),
		scalarMember(`{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`,
			true, trq),
	})
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{}
	r := newTestMergeRequest(t)
	w := httptest.NewRecorder()
	h.serveStandard(w, r, p.Targets(), request.GetResources(r),
		tsmerge.StrategyScalar, nil, trq.Statement, nil, "")

	const want = `{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", w.Code, http.StatusOK)
	}
}

func TestServeStandardScalarInstantPrefersFirstNonNaN(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{Statement: "scalar(count(up))"}
	p, _, _ := albpool.NewHealthy([]http.Handler{
		scalarMember(`{"status":"success","data":{"resultType":"scalar","result":[100,"NaN"]}}`, true, trq),
		scalarMember(`{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`, true, trq),
		scalarMember(`{"status":"success","data":{"resultType":"scalar","result":[102,"99"]}}`, true, trq),
	})
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{}
	r := newTestMergeRequest(t)
	w := httptest.NewRecorder()
	h.serveStandard(w, r, p.Targets(), request.GetResources(r),
		tsmerge.StrategyScalar, nil, trq.Statement, nil, "")

	const want = `{"status":"success","data":{"resultType":"scalar","result":[101,"42"]}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
}

func TestServeStandardScalarRangeCollapsesFanoutSamples(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{
		Statement: "scalar(count(up))",
		Extent: timeseries.Extent{
			Start: time.Unix(100, 0),
			End:   time.Unix(115, 0),
		},
		Step: 15 * time.Second,
	}
	matrix := func(first, second string) string {
		return fmt.Sprintf(`{"status":"success","data":{"resultType":"matrix","result":[`+
			`{"metric":{},"values":[[100,%q],[115,%q]]}]}}`, first, second)
	}
	p, _, _ := albpool.NewHealthy([]http.Handler{
		scalarMember(matrix("NaN", "NaN"), false, trq),
		scalarMember(matrix("42", "43"), false, trq),
		scalarMember(matrix("99", "100"), false, trq),
	})
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{}
	r := newTestMergeRequest(t)
	w := httptest.NewRecorder()
	h.serveStandard(w, r, p.Targets(), request.GetResources(r),
		tsmerge.StrategyScalar, nil, trq.Statement, nil, "")

	const want = `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{},"values":[[100,"42"],[115,"43"]]}]}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
}

func TestServeStandardScalarRangeIgnoresErrorEnvelope(t *testing.T) {
	trq := &timeseries.TimeRangeQuery{
		Statement: "scalar(count(up)) + 1",
		Extent: timeseries.Extent{
			Start: time.Unix(100, 0),
			End:   time.Unix(115, 0),
		},
		Step: 15 * time.Second,
	}
	p, _, _ := albpool.NewHealthy([]http.Handler{
		scalarMemberStatus(`{"status":"error","errorType":"bad_data","error":"boom"}`,
			false, trq, http.StatusInternalServerError),
		scalarMember(`{"status":"success","data":{"resultType":"matrix","result":[`+
			`{"metric":{},"values":[[100,"42"],[115,"43"]]}]}}`, false, trq),
	})
	defer p.Stop()
	p.RefreshHealthy()

	h := &handler{}
	r := newTestMergeRequest(t)
	w := httptest.NewRecorder()
	h.serveStandard(w, r, p.Targets(), request.GetResources(r),
		tsmerge.StrategyScalar, nil, trq.Statement, nil, "")

	const want = `{"status":"success","data":{"resultType":"matrix","result":[` +
		`{"metric":{},"values":[[100,"42"],[115,"43"]]}]}}`
	if got := w.Body.String(); got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want %d", w.Code, http.StatusOK)
	}
}
