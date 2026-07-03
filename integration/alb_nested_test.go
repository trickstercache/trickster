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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/integration/promstub"
)

// Issue #996: an ALB whose pool contains another ALB ("nested") was reported
// on the /trickster/health page as "nc:[alb-inner]" because the inner ALB has
// no health check registered. The user could not tell whether traffic was
// actually flowing. This test pins both behaviors:
//   - alb-inner appears in alb-outer's availablePoolMembers (not unchecked)
//   - traffic through alb-outer reaches alb-inner's leaf members
func TestALBNestedPoolAvailable(t *testing.T) {
	promRange := albTestdata(t, "alb_nested/prom_range.json.tmpl")

	mkRange := func(start, end, step int64) string {
		var b strings.Builder
		first := true
		for ts := start; ts <= end; ts += step {
			if !first {
				b.WriteString(",")
			}
			first = false
			fmt.Fprintf(&b, `[%d,"1"]`, ts)
		}
		return fmt.Sprintf(promRange, b.String())
	}

	var aHits, bHits atomic.Int64
	mk := func(hits *atomic.Int64) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == promstub.BuildInfoPath {
				promstub.WriteBuildInfo(w)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = r.ParseForm()
			start, _ := parseInt(r.Form.Get("start"))
			end, _ := parseInt(r.Form.Get("end"))
			step, _ := parseInt(r.Form.Get("step"))
			if step == 0 {
				step = 15
			}
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, mkRange(start, end, step))
		}))
	}
	leafA := mk(&aHits)
	t.Cleanup(leafA.Close)
	leafB := mk(&bHits)
	t.Cleanup(leafB.Close)

	const (
		frontPort   = 18960
		metricsPort = 18961
		mgmtPort    = 18962
	)

	yaml := fmt.Sprintf(albTestdata(t, "alb_nested/nested.yaml.tmpl"),
		frontPort, metricsPort, mgmtPort, leafA.URL, leafB.URL)

	cfgPath := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(yaml), 0644))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go startTrickster(t, ctx, expectedStartError{}, "-config", cfgPath)

	metricsAddr := fmt.Sprintf("127.0.0.1:%d", metricsPort)
	waitForTrickster(t, metricsAddr)

	healthURL := "http://" + metricsAddr + "/trickster/health"
	requireHealthState(t, healthURL, "prom-a", "available", 10*time.Second)
	requireHealthState(t, healthURL, "prom-b", "available", 10*time.Second)

	requireALBMemberState(t, healthURL, "alb-outer", "alb-inner", "available", 10*time.Second)
	requireALBMemberNotIn(t, healthURL, "alb-outer", "alb-inner", "unchecked")

	now := time.Now()
	end := now.Truncate(15 * time.Second)
	start := end.Add(-2 * time.Minute)
	params := url.Values{
		"query": {fmt.Sprintf("up + 0*%d", now.UnixNano())},
		"start": {fmt.Sprintf("%d", start.Unix())},
		"end":   {fmt.Sprintf("%d", end.Unix())},
		"step":  {"15"},
	}
	u := fmt.Sprintf("http://127.0.0.1:%d/alb-outer/api/v1/query_range?%s",
		frontPort, params.Encode())

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		r, err := http.Get(u)
		if !assert.NoError(c, err) {
			return
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		assert.Equal(c, http.StatusOK, r.StatusCode, "body=%s", string(b))
		assert.Greater(c, aHits.Load()+bHits.Load(), int64(0),
			"waiting for nested alb to fan traffic to a leaf")
	}, 10*time.Second, 200*time.Millisecond,
		"alb-outer never routed traffic through alb-inner to leaf upstreams")
}

// requireALBMemberNotIn asserts the named member does not appear in the given
// bucket on the named ALB. bucket is one of "available", "unavailable",
// "unchecked", "initializing".
func requireALBMemberNotIn(t *testing.T, healthURL, alb, member, bucket string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, healthURL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	type entry struct {
		Name                    string   `json:"name"`
		AvailablePoolMembers    []string `json:"availablePoolMembers"`
		UnavailablePoolMembers  []string `json:"unavailablePoolMembers"`
		UncheckedPoolMembers    []string `json:"uncheckedPoolMembers"`
		InitializingPoolMembers []string `json:"initializingPoolMembers"`
	}
	var hs struct {
		Available   []entry `json:"available"`
		Unavailable []entry `json:"unavailable"`
	}
	require.NoError(t, json.Unmarshal(b, &hs), "health payload was not JSON: %s", string(b))
	all := append([]entry{}, hs.Available...)
	all = append(all, hs.Unavailable...)
	for _, e := range all {
		if e.Name != alb {
			continue
		}
		var pool []string
		switch bucket {
		case "available":
			pool = e.AvailablePoolMembers
		case "unavailable":
			pool = e.UnavailablePoolMembers
		case "unchecked":
			pool = e.UncheckedPoolMembers
		case "initializing":
			pool = e.InitializingPoolMembers
		}
		require.NotContains(t, pool, member,
			"member %q must not appear in ALB %q %sPoolMembers (body=%s)",
			member, alb, bucket, string(b))
		return
	}
	t.Fatalf("ALB %q not present in health page (body=%s)", alb, string(b))
}
