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
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {

	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	expected := "query=select+%28intdiv%28touint32%28myTimeField%29%2C+" +
		"60%29+%2A+60%29+%2A+where+myTimeField+BETWEEN+toDateTime%28" +
		fmt.Sprintf("%d", start.Unix()) + "%29+AND+toDateTime%28" +
		fmt.Sprintf("%d", end.Unix()) + "%29+end"

	client := &Client{}

	tu := &url.URL{}
	e := &timeseries.Extent{Start: start, End: end}

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{
		TemplateURL: tu,
		Statement:   `select (intdiv(touint32(myTimeField), 60) * 60) * where myTimeField BETWEEN toDateTime(<$TS1$>) AND toDateTime(<$TS2$>) end`,
	}
	tu.RawQuery = url.Values{"query": []string{trq.Statement}}.Encode()

	client.SetExtent(r, trq, e)
	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot      [%s]", expected, r.URL.RawQuery)
	}

	client.SetExtent(r, trq, nil)
	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot      [%s]", expected, r.URL.RawQuery)
	}

}
