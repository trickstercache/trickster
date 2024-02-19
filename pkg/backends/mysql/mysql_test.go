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

package mysql

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/mysql/model"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registration"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

var testModeler = model.NewModeler()

func TestClientInterfacing(t *testing.T) {

	// this test ensures the client will properly conform to the
	// Client and TimeseriesBackend interfaces

	c, err := backends.NewTimeseriesBackend("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	var oc backends.Backend = c
	var tc backends.TimeseriesBackend = c

	if oc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", oc.Name())
	}

	if tc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", tc.Name())
	}
}

func TestNewClient(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-provider", "mysql", "-origin-url", "http://1"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	o := &bo.Options{Provider: "TEST_CLIENT"}
	c, err := NewClient("default", o, nil, cache, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Provider != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Provider)
	}

	if c.Configuration().Provider != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Provider)
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	type test_query struct {
		name string
		raw  string
		pass bool
		step time.Duration
		over time.Duration
	}
	tests := []test_query{
		{"basic", tq00, true, 60 * time.Second, 26 * time.Hour},
		{"between", tq01, true, 60 * time.Second, 26 * time.Hour},
		{"order-by", tq02, true, 60 * time.Second, 26 * time.Hour},
		{"count", tq03, true, 30 * time.Second, 26 * time.Hour},
		{"limit", tq04, false, 0, 0},
		{"nostep", tq05, false, 0, 0},
		{"empty", "", false, 0, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := &http.Request{URL: &url.URL{
				Scheme:   "https",
				Host:     "blah.com",
				Path:     "/",
				RawQuery: testQuery(test.raw),
			},
				Header: http.Header{},
			}
			client := &Client{}
			res, _, _, err := client.ParseTimeRangeQuery(req)
			if err != nil {
				if test.pass {
					t.Error(err)
				}
			} else {
				if !test.pass {
					t.Error("expected parsing to fail")
				}
				if res.Step != test.step {
					t.Errorf("expected step %v, got %v", test.step, res.Step)
				}
				if res.Extent.End.Sub(res.Extent.Start) != test.over {
					t.Errorf("expected query over duration %v, got %v", test.over, res.Extent.End.Sub(res.Extent.Start))
				}
			}
		})
	}
}
