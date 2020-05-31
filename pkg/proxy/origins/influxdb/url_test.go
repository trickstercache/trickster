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
	"io/ioutil"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {

	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	expected := "q=select+%2A+where+time+%3E%3D+" +
		fmt.Sprintf("%d", start.Unix()*1000) +
		"ms+AND+time+%3C%3D+" + fmt.Sprintf("%d", end.Unix()*1000) + "ms+group+by+time%281m%29"

	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090", "-origin-type", "influxdb", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	client := Client{config: oc}

	const tokenized = "q=select * where <$TIME_TOKEN$> group by time(1m)"

	tu := &url.URL{RawQuery: tokenized}

	r, _ := http.NewRequest(http.MethodGet, tu.String(), nil)
	trq := &timeseries.TimeRangeQuery{TemplateURL: tu}
	e := &timeseries.Extent{Start: start, End: end}
	client.SetExtent(r, trq, e)

	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, r.URL.RawQuery)
	}

	r.Method = http.MethodPost
	r.Body = ioutil.NopCloser(bytes.NewBufferString(tokenized))
	client.SetExtent(r, trq, e)
	_, s, _ := request.GetRequestValues(r)
	if expected != s {
		t.Errorf("\nexpected [%s]\ngot    [%s]", expected, s)
	}

}
