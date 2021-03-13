/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/tricksterproxy/trickster/cmd/trickster/config"
	"github.com/tricksterproxy/trickster/pkg/proxy/params"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

const expectedTokenized = "SELECT * FROM some_column WHERE time >= '$START_TIME$' AND time < '$END_TIME$' GROUP BY time(1m)"

func TestSetExtent(t *testing.T) {

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

	client, err := NewClient("default", o, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	const tokenized = "q=select * FROM some_column where time >= now() - 6h group by time(1m)"

	tu := &url.URL{RawQuery: tokenized}

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{TemplateURL: tu, Step: time.Second * 60}
	e := &timeseries.Extent{Start: start, End: end}
	client.SetExtent(r, trq, e)

	if expected != r.URL.Query().Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, r.URL.Query().Get("q"))
	}

	r.Method = http.MethodPost
	r.Body = io.NopCloser(bytes.NewBufferString(tokenized))
	client.SetExtent(r, trq, e)
	v, _, _ := params.GetRequestValues(r)

	fmt.Println("V", v)

	if expected != v.Get("q") {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, v.Get("q'"))
	}

}
