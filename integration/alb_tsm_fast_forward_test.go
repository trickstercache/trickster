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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

func TestALBTSMFastForwardUsesRangeEnd(t *testing.T) {
	type observedTimes struct {
		sync.Mutex
		values []string
	}
	snapshot := func(observed *observedTimes) []string {
		observed.Lock()
		defer observed.Unlock()
		return append([]string(nil), observed.values...)
	}

	newOrigin := func(value string, implicitTime int64) (*httptest.Server, *observedTimes) {
		observed := &observedTimes{}
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == promstub.BuildInfoPath {
				promstub.WriteBuildInfo(w)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			switch {
			case strings.HasSuffix(r.URL.Path, "/query_range"):
				start, err := strconv.ParseInt(r.Form.Get("start"), 10, 64)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				end, err := strconv.ParseInt(r.Form.Get("end"), 10, 64)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				step, err := strconv.ParseInt(r.Form.Get("step"), 10, 64)
				if err != nil || step <= 0 {
					http.Error(w, "invalid step", http.StatusBadRequest)
					return
				}

				var points strings.Builder
				for ts := start; ts <= end; ts += step {
					if points.Len() > 0 {
						points.WriteByte(',')
					}
					fmt.Fprintf(&points, `[%d,%q]`, ts, value)
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[`+
					`{"metric":{"__name__":"example_counter"},"values":[%s]}]}}`, points.String())

			case strings.HasSuffix(r.URL.Path, "/query"):
				evaluationTime := r.Form.Get("time")
				observed.Lock()
				observed.values = append(observed.values, evaluationTime)
				observed.Unlock()

				sampleTime := evaluationTime
				if sampleTime == "" {
					sampleTime = strconv.FormatInt(implicitTime, 10)
				}
				fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[`+
					`{"metric":{"__name__":"example_counter"},"value":[%s,%q]}]}}`, sampleTime, value)

			default:
				http.NotFound(w, r)
			}
		})
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return srv, observed
	}

	step := time.Hour
	requestedEnd := time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second)
	requestedEndUnix := requestedEnd.Unix()
	originA, timesA := newOrigin("10", requestedEndUnix+10)
	originB, timesB := newOrigin("8", requestedEndUnix+20)

	ports, releasePorts := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]
	var cfg strings.Builder
	cfg.WriteString(promstub.Preamble(frontPort, metricsPort, mgmtPort))
	cfg.WriteString("backends:\n")
	cfg.WriteString(promstub.BackendStanza("prom-a", originA.URL))
	cfg.WriteString(promstub.BackendStanza("prom-b", originB.URL))
	cfg.WriteString("  alb-tsm:\n")
	cfg.WriteString("    provider: alb\n")
	cfg.WriteString("    alb:\n")
	cfg.WriteString("      mechanism: tsm\n")
	cfg.WriteString("      output_format: prometheus\n")
	cfg.WriteString("      pool:\n")
	cfg.WriteString("        - prom-a\n")
	cfg.WriteString("        - prom-b\n")

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg.String()), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	releasePorts()
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)
	waitForTrickster(t, fmt.Sprintf("127.0.0.1:%d", metricsPort))
	healthURL := fmt.Sprintf("http://127.0.0.1:%d/trickster/health", metricsPort)
	requireHealthState(t, healthURL, "prom-a", "available", 10*time.Second)
	requireHealthState(t, healthURL, "prom-b", "available", 10*time.Second)

	params := url.Values{
		"query": {"sum(example_counter)"},
		"start": {strconv.FormatInt(requestedEnd.Add(-2*step).Unix(), 10)},
		"end":   {strconv.FormatInt(requestedEndUnix, 10)},
		"step":  {strconv.FormatInt(int64(step.Seconds()), 10)},
	}
	response, _ := queryTricksterProm(t,
		fmt.Sprintf("127.0.0.1:%d", frontPort), "alb-tsm", "/api/v1/query_range", params)

	var data promQueryData
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.Equal(t, "matrix", data.ResultType)
	var series []struct {
		Values [][]json.RawMessage `json:"values"`
	}
	require.NoError(t, json.Unmarshal(data.Result, &series))
	require.Len(t, series, 1)

	var fastForwardPoints [][]json.RawMessage
	for _, point := range series[0].Values {
		var epoch float64
		require.NoError(t, json.Unmarshal(point[0], &epoch))
		require.LessOrEqual(t, int64(epoch), requestedEndUnix)
		if int64(epoch) == requestedEndUnix {
			fastForwardPoints = append(fastForwardPoints, point)
		}
	}
	require.Len(t, fastForwardPoints, 1)

	var epoch float64
	var value string
	require.NoError(t, json.Unmarshal(fastForwardPoints[0][0], &epoch))
	require.NoError(t, json.Unmarshal(fastForwardPoints[0][1], &value))
	require.Equal(t, requestedEndUnix, int64(epoch))
	require.Equal(t, "18", value)

	wantTime := strconv.FormatInt(requestedEndUnix, 10)
	require.Equal(t, []string{wantTime}, snapshot(timesA))
	require.Equal(t, []string{wantTime}, snapshot(timesB))
}
