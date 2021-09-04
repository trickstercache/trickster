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

package clickhouse

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/model"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registration"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

var testModeler = model.NewModeler()

func TestClickhouseClientInterfacing(t *testing.T) {

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

	conf, _, err := config.Load("trickster", "test", []string{"-provider", "clickhouse", "-origin-url", "http://1"})
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
	req := &http.Request{URL: &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: testRawQuery(),
	},
		Header: http.Header{},
	}
	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if res.Step.Seconds() != 60 {
			t.Errorf("expected 60 got %f", res.Step.Seconds())
		}
		if res.Extent.End.Sub(res.Extent.Start).Hours() != 6 {
			t.Errorf("expected 6 got %f", res.Extent.End.Sub(res.Extent.Start).Hours())
		}
	}

	req.URL.RawQuery = ""
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf("expected error for: %s", "missing URL parameter: [query]")
	}

	req.URL.RawQuery = url.Values(map[string][]string{"query": {
		`SELECT (intDiv(toUInt32(abc), 6z0) * 6z0) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
			`FROM testdb.test_table WHERE abc BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
			`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
			`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`}}).Encode()
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf("expected error for: %s", "not a time range query")
	}

	req.URL.RawQuery = url.Values(map[string][]string{"query": {
		`SELECT (intDiv(toUInt32(0^^^), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
			`FROM testdb.test_table WHERE 0^^^ BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
			`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
			`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`}}).Encode()
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf("expected error for: %s", "not a time range query")
	}

}
