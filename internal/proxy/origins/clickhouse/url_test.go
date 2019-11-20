/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestSetExtent(t *testing.T) {

	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	expected := "query=select+%28intdiv%28touint32%28myTimeField%29%2C+60%29+%2A+60%29+%2A+where+myTimeField+BETWEEN+toDateTime%28" +
		fmt.Sprintf("%d", start.Unix()) + "%29+AND+toDateTime%28" + fmt.Sprintf("%d", end.Unix()) + "%29+end"

	client := &Client{}
	u := &url.URL{}
	tu := &url.URL{RawQuery: "query=select (intdiv(touint32(myTimeField), 60) * 60) * where myTimeField BETWEEN toDateTime(<$TIMESTAMP1$>) AND toDateTime(<$TIMESTAMP2$>) end"}
	r := &model.Request{URL: u, TemplateURL: tu, TimeRangeQuery: &timeseries.TimeRangeQuery{TimestampFieldName: "myTimeField"}}
	e := &timeseries.Extent{Start: start, End: end}
	client.SetExtent(r, e)

	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot      [%s]", expected, r.URL.RawQuery)
	}
}

func TestBuildUpstreamURL(t *testing.T) {

	cfg := config.NewConfig()
	oc := cfg.Origins["default"]
	oc.Scheme = "http"
	oc.Host = "0"
	oc.PathPrefix = ""

	client := &Client{name: "default", config: oc}
	r, err := http.NewRequest(http.MethodGet, "http://0/default/?query=SELECT+1+FORMAT+JSON", nil)
	if err != nil {
		t.Error(err)
	}
	client.BuildUpstreamURL(r)

}
