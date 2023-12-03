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

package influxdb

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const expectedTokenized = "SELECT * FROM some_column WHERE time >= '$START_TIME$' AND time < '$END_TIME$' GROUP BY time(1m)"

func TestSetExtentInfluxQL(t *testing.T) {

	start := time.Now().UTC().Add(time.Duration(-6) * time.Hour).Truncate(time.Second)
	end := time.Now().UTC().Truncate(time.Second)

	startToken := start.Format(time.RFC3339Nano)
	endToken := end.Add(time.Second * 60).Format(time.RFC3339Nano)

	expected := strings.Replace(strings.Replace(expectedTokenized, "$START_TIME$", startToken, -1), "$END_TIME$", endToken, -1)

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090", "-provider", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]

	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	const tokenized = "q=SELECT * FROM some_column WHERE time >= now() - 6h GROUP BY time(1m)"

	tu := &url.URL{RawQuery: tokenized}

	ic := client.(*Client)

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{TemplateURL: tu, Step: time.Second * 60}
	e := &timeseries.Extent{Start: start, End: end}
	ic.SetExtent(r, trq, e)

	if expected != r.URL.Query().Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, r.URL.Query().Get("q"))
	}

	const body = "q=SELECT * FROM some_column WHERE time >= now() - 6h GROUP BY time(1m)"

	r.Method = http.MethodPost
	r.Body = io.NopCloser(bytes.NewBufferString(body))
	ic.SetExtent(r, trq, e)
	v, _, _ := params.GetRequestValues(r)

	if expected != v.Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, v.Get("q"))
	}

}

var testQuery = `from("test-bucket")
|> $RANGE
|> window(every: 1m)
`

func TestSetExtentFlux(t *testing.T) {
	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090", "-provider", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]

	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ic := client.(*Client)

	start := time.Now().Add(-1 * time.Hour)
	end := time.Now()

	r, _ := http.NewRequest(http.MethodGet, "", nil)
	r.Method = http.MethodPost
	r.Header.Add(headers.NameContentType, headers.ValueApplicationFlux)
	body := strings.Replace(testQuery, "$RANGE", "range(start: -7d, stop: -6d)", 1)
	r.Body = io.NopCloser(bytes.NewBufferString(body))
	trq := &timeseries.TimeRangeQuery{Step: time.Second * 60}
	e := &timeseries.Extent{Start: start, End: end}
	ic.SetExtent(r, trq, e)

	newRange := fmt.Sprintf("range(start: %s, stop: %s)", start.Format(time.RFC3339), end.Format(time.RFC3339))
	expected := strings.Replace(testQuery, "$RANGE", newRange, 1)
	b, _ := io.ReadAll(r.Body)
	if string(b) != expected {
		t.Errorf("expected %s, got %s", expected, string(b))
	}
}
