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

package integration

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const albAddr = "127.0.0.1:8490"

func TestALB(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/alb.yaml")
	waitForTrickster(t, "127.0.0.1:8491")
	waitForPrometheusData(t, "127.0.0.1:9090")

	rangeParams := func() url.Values {
		now := time.Now()
		return url.Values{
			"query": {"up"},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"15"},
		}
	}

	t.Run("FR range query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fr", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		require.NotEmpty(t, qd.Result, "fr range query should return a non-empty matrix")
		t.Logf("fr range: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("FR instant query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fr", "/api/v1/query",
			url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result, "fr instant query should return a non-empty vector")
		t.Logf("fr instant: %s", hdr.Get("X-Trickster-Result"))
	})

	basic := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}

	doURQuery := func(t *testing.T, authz, path string, params url.Values) (promQueryData, http.Header) {
		t.Helper()
		u := "http://" + albAddr + "/alb-ur" + path
		if len(params) > 0 {
			u += "?" + params.Encode()
		}
		req, err := http.NewRequest(http.MethodGet, u, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", authz)
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"expected 200 from alb-ur, got %d", resp.StatusCode)
		var pr promResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		return qd, resp.Header.Clone()
	}

	extractRegions := func(t *testing.T, qd promQueryData) map[string]bool {
		var series []struct {
			Metric map[string]string `json:"metric"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series, "alb-ur query must return at least one series")
		regions := make(map[string]bool)
		for _, s := range series {
			regions[s.Metric["region"]] = true
		}
		return regions
	}

	instantParams := url.Values{"query": {"up"}}
	urRangeParams := func() url.Values {
		now := time.Now()
		return url.Values{
			"query": {"up"},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"60"},
		}
	}

	t.Run("UR alice instant routes to us-east", func(t *testing.T) {
		qd, hdr := doURQuery(t, basic("alice", "alicepw"), "/api/v1/query", instantParams)
		regions := extractRegions(t, qd)
		require.True(t, regions["us-east"],
			"alice should be routed to prom1-labeled (region=us-east); got %v", regions)
		require.False(t, regions["us-west"],
			"alice must not see prom2-labeled's us-west label; got %v", regions)
		t.Logf("ur alice instant: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("UR bob instant routes to us-west", func(t *testing.T) {
		qd, hdr := doURQuery(t, basic("bob", "bobpw"), "/api/v1/query", instantParams)
		regions := extractRegions(t, qd)
		require.True(t, regions["us-west"],
			"bob should be routed to prom2-labeled (region=us-west); got %v", regions)
		require.False(t, regions["us-east"],
			"bob must not see prom1-labeled's us-east label; got %v", regions)
		t.Logf("ur bob instant: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("UR alice range routes to us-east", func(t *testing.T) {
		qd, hdr := doURQuery(t, basic("alice", "alicepw"), "/api/v1/query_range", urRangeParams())
		require.Equal(t, "matrix", qd.ResultType)
		regions := extractRegions(t, qd)
		require.True(t, regions["us-east"],
			"alice range query should route to prom1-labeled; got %v", regions)
		require.False(t, regions["us-west"],
			"alice range query must not see us-west; got %v", regions)
		t.Logf("ur alice range: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("UR bob range routes to us-west", func(t *testing.T) {
		qd, hdr := doURQuery(t, basic("bob", "bobpw"), "/api/v1/query_range", urRangeParams())
		require.Equal(t, "matrix", qd.ResultType)
		regions := extractRegions(t, qd)
		require.True(t, regions["us-west"],
			"bob range query should route to prom2-labeled; got %v", regions)
		require.False(t, regions["us-east"],
			"bob range query must not see us-east; got %v", regions)
		t.Logf("ur bob range: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("FGR header propagation", func(t *testing.T) {
		backend := "alb-fgr-labeled"
		q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano())
		_, hdr := queryTricksterProm(t, albAddr, backend, "/api/v1/query",
			url.Values{"query": {q}})
		require.Equal(t, "prom1", hdr.Get("X-Test-Origin"),
			"%s: backend-emitted response headers must survive the ALB FGR transform path (issue #970)",
			backend)
		t.Logf("%s headers: %v", backend, hdr)
	})

	t.Run("TSM header propagation", func(t *testing.T) {
		backend := "alb-tsm-labeled"
		q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano())
		_, hdr := queryTricksterProm(t, albAddr, backend, "/api/v1/query",
			url.Values{"query": {q}})
		require.Equal(t, "prom1", hdr.Get("X-Test-Origin"),
			"%s: TSM merge path must preserve custom response headers from pool members (issue #970)",
			backend)
		t.Logf("%s headers: %v", backend, hdr)
	})

	assertAggregationCollapses := func(t *testing.T, qd promQueryData) {
		t.Helper()
		require.NotEmpty(t, qd.Result,
			"tsm merge must return a non-empty result for `sum by (job) (up)` (issue #956)")

		var series []struct {
			Metric map[string]string `json:"metric"`
		}
		require.NoError(t, json.Unmarshal(qd.Result, &series))
		require.NotEmpty(t, series,
			"aggregation merge must not drop all rows (issue #956)")

		jobs := make(map[string]int)
		for _, s := range series {
			require.NotEmpty(t, s.Metric["job"],
				"each `sum by (job)` result series must carry a job label (issue #956); got %v",
				s.Metric)
			require.Empty(t, s.Metric["region"],
				"aggregation merge must strip injected labels before hashing; "+
					"series still carries region=%q (issue #956)", s.Metric["region"])
			jobs[s.Metric["job"]]++
		}
		for job, n := range jobs {
			require.Equal(t, 1, n,
				"job=%q appears %d times in sum-by aggregation; rows failed to collapse (issue #956)",
				job, n)
		}
	}

	t.Run("TSM aggregation merge range", func(t *testing.T) {
		now := time.Now()
		queryExpr := fmt.Sprintf("sum by (job) (up + 0*%d)", now.UnixNano())
		rangeVals := url.Values{
			"query": {queryExpr},
			"start": {fmt.Sprintf("%d", now.Add(-5*time.Minute).Unix())},
			"end":   {fmt.Sprintf("%d", now.Unix())},
			"step":  {"60"},
		}
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm-labeled", "/api/v1/query_range", rangeVals)
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		assertAggregationCollapses(t, qd)
		t.Logf("tsm aggregation merge (range): %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("TSM aggregation merge instant", func(t *testing.T) {
		queryExpr := fmt.Sprintf("sum by (job) (up + 0*%d)", time.Now().UnixNano())
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm-labeled", "/api/v1/query",
			url.Values{"query": {queryExpr}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType,
			"instant aggregation through TSM must emit vector envelope, not matrix")
		assertAggregationCollapses(t, qd)
		t.Logf("tsm aggregation merge (instant): %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("TSM aggregation merge instant POST", func(t *testing.T) {
		queryExpr := fmt.Sprintf("sum by (job) (up + 0*%d)", time.Now().UnixNano())
		form := url.Values{"query": {queryExpr}}
		u := "http://" + albAddr + "/alb-tsm-labeled/api/v1/query"
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Post(u, "application/x-www-form-urlencoded",
			strings.NewReader(form.Encode()))
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode,
			"unexpected status %d: %s", resp.StatusCode, string(body))
		var pr promResponse
		require.NoError(t, json.Unmarshal(body, &pr))
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType,
			"POST instant aggregation through TSM must still emit vector envelope")
		assertAggregationCollapses(t, qd)
		t.Logf("tsm aggregation merge (instant POST): %s", resp.Header.Get("X-Trickster-Result"))
	})

	t.Run("TSM deflate origin", func(t *testing.T) {
		deflateBody := `{"status":"success","data":{"resultType":"vector","result":[` +
			`{"metric":{"__name__":"up","job":"fake","instance":"deflate:1"},` +
			`"value":[1700000000,"1"]}]}}`
		var buf bytes.Buffer
		fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
		require.NoError(t, err)
		_, err = fw.Write([]byte(deflateBody))
		require.NoError(t, err)
		require.NoError(t, fw.Close())
		deflated := buf.Bytes()

		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "deflate")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(deflated)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		})

		l, err := net.Listen("tcp", "127.0.0.1:18500")
		if err != nil {
			t.Skipf("TODO(#938): port 18500 unavailable (%v); deflate-origin test skipped", err)
			return
		}
		srv := &httptest.Server{
			Listener: l,
			Config:   &http.Server{Handler: mux},
		}
		srv.Start()
		t.Cleanup(srv.Close)

		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm-deflate", "/api/v1/query",
			url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result,
			"tsm merge must decode deflate-encoded upstream and return a non-empty merged vector (issue #938)")
		t.Logf("tsm deflate origin: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("FR cancel race", func(t *testing.T) {
		const iterations = 50
		u := "http://" + albAddr + "/alb-fr/api/v1/query?query=up"

		var wg sync.WaitGroup
		for i := 0; i < iterations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := context.WithCancel(context.Background())
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
				if err != nil {
					cancel()
					return
				}
				go cancel()
				resp, err := http.DefaultClient.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}()
		}
		wg.Wait()
	})

	t.Run("fgr range query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fgr", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("fgr range: %s", hdr.Get("X-Trickster-Result"))
		require.NotEmpty(t, result["engine"])

		_, hdr2 := queryTricksterProm(t, albAddr, "alb-fgr", "/api/v1/query_range", rangeParams())
		t.Logf("fgr range (repeat): %s", hdr2.Get("X-Trickster-Result"))
	})

	t.Run("fgr instant query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-fgr", "/api/v1/query", url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		t.Logf("fgr instant: %s", hdr.Get("X-Trickster-Result"))
	})

	for _, mech := range []string{"fgr", "nlm", "tsm"} {
		t.Run(mech+" large instant response survives proxy", func(t *testing.T) {
			params := url.Values{"query": {`{__name__=~".+"}`}}
			pr, hdr := queryTricksterProm(t, albAddr, "alb-"+mech, "/api/v1/query", params)
			require.Equal(t, "success", pr.Status)
			require.Greater(t, len(pr.Data), 32*1024)
			var qd promQueryData
			require.NoError(t, json.Unmarshal(pr.Data, &qd))
			require.Equal(t, "vector", qd.ResultType)
			t.Logf("%s: %d bytes, %s", mech, len(pr.Data), hdr.Get("X-Trickster-Result"))
		})
	}

	t.Run("rr multiple requests", func(t *testing.T) {
		for i := range 3 {
			pr, hdr := queryTricksterProm(t, albAddr, "alb-rr", "/api/v1/query_range", rangeParams())
			require.Equal(t, "success", pr.Status)
			require.NotEmpty(t, hdr.Get("X-Trickster-Result"))
			t.Logf("rr range request %d: %s", i, hdr.Get("X-Trickster-Result"))
		}
	})

	t.Run("rr instant query", func(t *testing.T) {
		for i := range 3 {
			pr, hdr := queryTricksterProm(t, albAddr, "alb-rr", "/api/v1/query",
				url.Values{"query": {"up"}})
			require.Equal(t, "success", pr.Status)
			var qd promQueryData
			require.NoError(t, json.Unmarshal(pr.Data, &qd))
			require.Equal(t, "vector", qd.ResultType)
			t.Logf("rr instant request %d: %s", i, hdr.Get("X-Trickster-Result"))
		}
	})

	t.Run("tsm range query merges", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		result := parseTricksterResult(hdr.Get("X-Trickster-Result"))
		t.Logf("tsm range: %s", hdr.Get("X-Trickster-Result"))
		require.NotEmpty(t, result["engine"])
	})

	t.Run("tsm instant query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/query", url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result, "instant query through TSM should return non-empty result")
		t.Logf("tsm instant: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("tsm labels merge", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/labels", nil)
		require.Equal(t, "success", pr.Status)
		var labels []string
		require.NoError(t, json.Unmarshal(pr.Data, &labels), "labels through TSM should return valid JSON array")
		require.Contains(t, labels, "job")
		require.Contains(t, labels, "__name__")
		t.Logf("tsm labels: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("tsm label values merge", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-tsm", "/api/v1/label/job/values", nil)
		require.Equal(t, "success", pr.Status)
		var values []string
		require.NoError(t, json.Unmarshal(pr.Data, &values), "label values through TSM should return valid JSON array")
		require.Contains(t, values, "prometheus")
		t.Logf("tsm label values: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("nlm range query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-nlm", "/api/v1/query_range", rangeParams())
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "matrix", qd.ResultType)
		t.Logf("nlm range: %s", hdr.Get("X-Trickster-Result"))
	})

	t.Run("nlm instant query", func(t *testing.T) {
		pr, hdr := queryTricksterProm(t, albAddr, "alb-nlm", "/api/v1/query",
			url.Values{"query": {"up"}})
		require.Equal(t, "success", pr.Status)
		var qd promQueryData
		require.NoError(t, json.Unmarshal(pr.Data, &qd))
		require.Equal(t, "vector", qd.ResultType)
		require.NotEmpty(t, qd.Result, "nlm instant query should return non-empty result")
		t.Logf("nlm instant: %s", hdr.Get("X-Trickster-Result"))
	})

	for _, mech := range []string{"fgr", "nlm", "tsm"} {
		t.Run(mech+"-labeled instant query (#937)", func(t *testing.T) {
			backend := "alb-" + mech + "-labeled"
			q := fmt.Sprintf("up + 0*%d", time.Now().UnixNano())
			pr, hdr := queryTricksterProm(t, albAddr, backend, "/api/v1/query", url.Values{"query": {q}})
			require.Equal(t, "success", pr.Status)
			require.Contains(t, hdr.Get("Content-Type"), "json",
				"%s instant query must advertise a JSON content type (issue #937)", backend)
			require.NotEmpty(t, hdr.Get("X-Trickster-Result"),
				"%s instant query must propagate X-Trickster-Result from the inner backend (issue #937)", backend)
			require.Empty(t, hdr.Get("Content-Encoding"),
				"%s instant query must not advertise stale upstream Content-Encoding (issue #937)", backend)
			var qd promQueryData
			require.NoError(t, json.Unmarshal(pr.Data, &qd))
			require.Equal(t, "vector", qd.ResultType)
			require.NotEmpty(t, qd.Result,
				"%s instant query should return a non-empty result", backend)
			var series []struct {
				Metric map[string]string `json:"metric"`
			}
			require.NoError(t, json.Unmarshal(qd.Result, &series))
			require.NotEmpty(t, series)
			for _, s := range series {
				require.NotEmpty(t, s.Metric["region"],
					"%s: each merged series should carry the injected region label", backend)
			}
			t.Logf("%s instant: %s", backend, hdr.Get("X-Trickster-Result"))
		})
	}
}
