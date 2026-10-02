/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	corpusPath    = "testdata/compatibility/v1.json"
	dashboardPath = "../../../docs/developer/environment/docker-compose-data/dashboards/trickster-victoriametrics-trips.json"
)

type corpus struct {
	SchemaVersion int `json:"schema_version"`
	Upstream      struct {
		Image     string `json:"image"`
		MetricsQL string `json:"metricsql"`
	} `json:"upstream"`
	Cases []struct {
		Name     string `json:"name"`
		Query    string `json:"query"`
		Expected struct {
			CacheMode string `json:"cache_mode"`
			Reason    string `json:"reason"`
		} `json:"expected"`
		Native struct {
			Status string `json:"status"`
		} `json:"native"`
	} `json:"cases"`
	Requests []corpusRequest `json:"requests"`
}

// corpusRequest is a request shape Grafana sent, with the path Trickster must give it.
type corpusRequest struct {
	Name     string            `json:"name"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Params   map[string]string `json:"params"`
	Expected struct {
		Cacheable bool   `json:"cacheable"`
		CacheMode string `json:"cache_mode"`
		Reason    string `json:"reason"`
	} `json:"expected"`
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile(corpusPath)
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.SchemaVersion != 1 || !strings.HasSuffix(c.Upstream.Image, ":v1.153.0") || c.Upstream.MetricsQL != "v0.87.5" {
		t.Fatalf("unexpected corpus header %+v", c.Upstream)
	}
	return c
}

var modeNames = map[cacheMode]string{modeDelta: cacheModeDelta, modeObject: cacheModeObject, modeProxy: cacheModeProxy}

func TestCompatibilityCorpus(t *testing.T) {
	c := loadCorpus(t)
	for _, tc := range c.Cases {
		a := classify(tc.Query)
		reason := a.reason
		if reason == "" {
			reason = reasonEligible
		}
		if modeNames[a.mode] != tc.Expected.CacheMode || reason != tc.Expected.Reason {
			t.Errorf("%s: got (%s, %s) want (%s, %s)", tc.Name, modeNames[a.mode], reason,
				tc.Expected.CacheMode, tc.Expected.Reason)
		}
		// whatever is relayed for a statement, VictoriaMetrics' own answer is what the client gets
		if tc.Native.Status == "error" && a.mode == modeDelta && tc.Name != "name_collision_error" {
			t.Errorf("%s: an evaluation error was expected only where it is relayed", tc.Name)
		}
	}
}

func TestCompatibilityGrafanaRequests(t *testing.T) {
	c := loadCorpus(t)
	client := newTestClient(t, "http://vm.example:8428", nil)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	for _, tc := range c.Requests {
		v := url.Values{}
		for k, p := range tc.Params {
			v.Set(k, strings.ReplaceAll(p, "$NOW", now))
		}
		r := newRequest(tc.Method, tc.Path, v)
		rt := routeOf(tc.Path)
		reason, ok := client.prepare(r, rt)
		mode := cacheModeProxy
		if ok {
			mode = cacheModeObject
			if rt == routeRange && reason == reasonEligible {
				mode = cacheModeDelta
			}
		}
		if ok != tc.Expected.Cacheable || reason != tc.Expected.Reason || mode != tc.Expected.CacheMode {
			t.Errorf("%s: got (%v, %s, %s) want %+v", tc.Name, ok, mode, reason, tc.Expected)
		}
	}
	if !slices.ContainsFunc(c.Requests, func(r corpusRequest) bool { return r.Method == http.MethodPost }) {
		t.Error("the corpus has no POST request")
	}
}

// grafanaVars are the expansions Grafana used for the dashboard's captured requests.
var grafanaVars = strings.NewReplacer("$__rate_interval", "2m", "$__interval", "120s", "$__range", "172800s")

// TestCorpusCoversDashboard requires every MetricsQL panel query of the VictoriaMetrics trips
// dashboard to be in the corpus, as Grafana expands it.
func TestCorpusCoversDashboard(t *testing.T) {
	c := loadCorpus(t)
	b, err := os.ReadFile(dashboardPath)
	if err != nil {
		t.Fatal(err)
	}
	var dash struct {
		Panels []struct {
			Datasource struct {
				UID string `json:"uid"`
			} `json:"datasource"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	if err := json.Unmarshal(b, &dash); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, tc := range c.Cases {
		known[tc.Query] = true
	}
	missing := 0
	for _, p := range dash.Panels {
		if p.Datasource.UID != "${datasource}" {
			continue
		}
		for _, target := range p.Targets {
			q := grafanaVars.Replace(target.Expr)
			if !known[q] {
				t.Errorf("dashboard query not in the corpus: %s", q)
				missing++
			}
		}
	}
	if missing == 0 && len(known) < 20 {
		t.Error("the corpus is unexpectedly small")
	}
}
