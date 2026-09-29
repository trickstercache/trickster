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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	"github.com/trickstercache/trickster/v2/integration/promstub"

	"github.com/stretchr/testify/require"
)

const (
	mixedTruncateMember = "prom-truncate"
	mixedOffMember      = "prom-off"
	mixedLeaderALB      = "tsm-leader"
	mixedOffALB         = "tsm-off"
	mixedStep           = 60
	mixedQueryRange     = "/api/v1/query_range"
)

func TestALBTSMAppliesOneStepAlignmentToMixedMembers(t *testing.T) {
	newOrigin := func(value string) *httptest.Server {
		// answers a range query at start + k*step, the grid the request asks for
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == promstub.BuildInfoPath {
				promstub.WriteBuildInfo(w)
				return
			}
			if err := r.ParseForm(); err != nil || !strings.HasSuffix(r.URL.Path, mixedQueryRange) {
				http.NotFound(w, r)
				return
			}
			start, _ := strconv.ParseFloat(r.Form.Get("start"), 64)
			end, _ := strconv.ParseFloat(r.Form.Get("end"), 64)
			step, _ := strconv.ParseInt(r.Form.Get("step"), 10, 64)
			if step <= 0 {
				http.Error(w, "invalid step", http.StatusBadRequest)
				return
			}
			var points strings.Builder
			for ts := int64(start); ts <= int64(end); ts += step {
				if points.Len() > 0 {
					points.WriteByte(',')
				}
				fmt.Fprintf(&points, `[%d,%q]`, ts, value)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"success","data":{"resultType":"matrix","result":[`+
				`{"metric":{"__name__":"example_counter"},"values":[%s]}]}}`, points.String())
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	stanza := func(name, originURL, mode string) string {
		return promstub.BackendStanza(name, originURL) + "    step_alignment: \"" + mode + "\"\n"
	}
	tsm := func(name, mode string) string {
		out := fmt.Sprintf("  %s:\n    provider: alb\n", name)
		if mode != "" {
			out += "    step_alignment: \"" + mode + "\"\n"
		}
		return out + fmt.Sprintf("    alb:\n      mechanism: tsm\n      output_format: prometheus\n"+
			"      pool: [%s, %s]\n", mixedTruncateMember, mixedOffMember)
	}

	ports, releasePorts := portutil.Reserve(t, 3)
	frontPort, metricsPort, mgmtPort := ports[0], ports[1], ports[2]
	var cfg strings.Builder
	cfg.WriteString(promstub.Preamble(frontPort, metricsPort, mgmtPort))
	cfg.WriteString("backends:\n")
	cfg.WriteString(stanza(mixedTruncateMember, newOrigin("10").URL, "truncate"))
	cfg.WriteString(stanza(mixedOffMember, newOrigin("8").URL, "off"))
	cfg.WriteString(tsm(mixedLeaderALB, ""))
	cfg.WriteString(tsm(mixedOffALB, "off"))
	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg.String()), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	releasePorts()
	runTrickster(t, ctx, "-config", cfgPath)
	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)
	healthURL := "http://" + metricsAddr + "/trickster/health"
	requireHealthState(t, healthURL, mixedTruncateMember, "available", 10*time.Second)
	requireHealthState(t, healthURL, mixedOffMember, "available", 10*time.Second)

	// a closed past range whose start sits 7s past the step grid
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Minute).Add(7 * time.Second)
	params := url.Values{
		"query": {"sum(example_counter)"},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(start.Add(30*time.Minute).Unix(), 10)},
		"step":  {strconv.Itoa(mixedStep)},
	}
	front := fmt.Sprintf("127.0.0.1:%d", frontPort)
	query := func(backend string) (phases map[int64]bool, values map[string]bool) {
		response, _ := queryTricksterProm(t, front, backend, mixedQueryRange, params)
		var data promQueryData
		require.NoError(t, json.Unmarshal(response.Data, &data))
		var series []struct {
			Values [][]json.RawMessage `json:"values"`
		}
		require.NoError(t, json.Unmarshal(data.Result, &series))
		require.Len(t, series, 1, backend)
		require.NotEmpty(t, series[0].Values, backend)
		phases, values = map[int64]bool{}, map[string]bool{}
		for _, point := range series[0].Values {
			var epoch float64
			var value string
			require.NoError(t, json.Unmarshal(point[0], &epoch))
			require.NoError(t, json.Unmarshal(point[1], &value))
			phases[int64(epoch)%mixedStep] = true
			values[value] = true
		}
		return phases, values
	}

	// asked directly, the members answer on different grids, which a merge would interleave
	phases, _ := query(mixedTruncateMember)
	require.Equal(t, map[int64]bool{0: true}, phases)
	phases, _ = query(mixedOffMember)
	require.Equal(t, map[int64]bool{7: true}, phases)

	for backend, want := range map[string]int64{mixedLeaderALB: 0, mixedOffALB: 7} {
		// every member answers on one grid, so each merged point sums both members
		phases, values := query(backend)
		require.Equal(t, map[int64]bool{want: true}, phases, backend)
		require.Equal(t, map[string]bool{"18": true}, values, backend)
	}
}
