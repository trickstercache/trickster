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

package resolution_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/graphite/resolution"
)

func TestLearnAgainstGraphiteWeb(t *testing.T) {
	// learns the dev environment's ladders from real graphite-web when
	// GRAPHITE_WEB_URL is set, checking them against the seeded schemas
	base := os.Getenv("GRAPHITE_WEB_URL")
	if base == "" {
		t.Skip("GRAPHITE_WEB_URL not set")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	origin := &resolution.Origin{Base: u, Client: http.DefaultClient, Timeout: 30 * time.Second}
	obs := newCounter()
	reg := resolution.NewRegistry(resolution.RegistryOptions{TTL: time.Hour, NegativeTTL: time.Second}, nil)
	exp := &resolution.Expander{Origin: origin, Registry: reg, Observer: obs, TTL: time.Minute}
	learner := &resolution.Learner{Prober: &resolution.Prober{Origin: origin, Observer: obs},
		Expander: exp, Registry: reg, Observer: obs, Budget: 96, Name: "devenv"}
	want := map[string]string{
		"dev.fast.cpu.host01.percent":            "10s:6h,1m:1w,10m:5y",
		"dev.medium.orders.us-east.count":        "1m:2d,5m:30d,1h:2y",
		"dev.coarse.users.active":                "5m:90d",
		"dev.drift.temperature.sensor01.celsius": "30s:12h,5m:2w,1h:1y",
	}
	for leaf, w := range want {
		before := obs.total()
		l, err := learner.Learn(context.Background(), leaf, nil)
		if err != nil {
			t.Fatalf("%s: %v", leaf, err)
		}
		if l.String() != w {
			t.Errorf("%s: learned %s want %s", leaf, l, w)
		}
		t.Logf("%s: %s in %d probes", leaf, l, obs.total()-before)
	}
	// the drift namespace: a confirming run against what the config claims
	// must detect the disagreement and relearn
	hint, _ := resolution.ParseRetentions("60s:2d,5m:30d,1h:2y")
	reg2 := resolution.NewRegistry(resolution.RegistryOptions{TTL: time.Hour, NegativeTTL: time.Second}, nil)
	learner.Registry = reg2
	l, err := learner.Learn(context.Background(), "dev.drift.temperature.sensor01.celsius", hint)
	if err != nil || l.String() != want["dev.drift.temperature.sensor01.celsius"] {
		t.Errorf("drift confirm: %v %v", l, err)
	}
	if e := exp.Exists(context.Background(), "dev.fast.cpu.nonexistent.percent"); e != resolution.NotExists {
		t.Errorf("existence check: %v", e)
	}
	if _, err := learner.Learn(context.Background(), "dev.fast.cpu.nonexistent.percent", nil); err == nil {
		t.Error("expected ErrMissingMetric")
	}
}
