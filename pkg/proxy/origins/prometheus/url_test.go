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

package prometheus

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestSetExtent(t *testing.T) {

	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()

	startSecs := fmt.Sprintf("%d", start.Unix())
	endSecs := fmt.Sprintf("%d", end.Unix())

	expected := "end=" + endSecs + "&q=up&start=" + startSecs

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "none:9090", "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	client := Client{config: oc}

	u := &url.URL{RawQuery: "q=up"}
	r, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	e := &timeseries.Extent{Start: start, End: end}
	client.SetExtent(r, nil, e)

	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, r.URL.RawQuery)
	}
}

func TestFastForwardURL(t *testing.T) {

	expected := "q=up"

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "none:9090", "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	client := Client{config: oc}

	u := &url.URL{Path: "/query_range", RawQuery: "q=up&start=1&end=1&step=1"}
	r, _ := http.NewRequest(http.MethodGet, u.String(), nil)

	u2, err := client.FastForwardURL(r)
	if err != nil {
		t.Error(err)
	}

	if expected != u2.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, u2.RawQuery)
	}

}

// func TestBuildUpstreamURL(t *testing.T) {

// 	cfg := config.NewConfig()
// 	oc := cfg.Origins["default"]
// 	oc.Scheme = "http"
// 	oc.Host = "0"
// 	oc.PathPrefix = ""

// 	client := &Client{name: "default", config: oc}
// 	r, err := http.NewRequest(http.MethodGet, "http://0/default/query_range", nil)
// 	if err != nil {
// 		t.Error(err)
// 	}
// 	client.BuildUpstreamURL(r)

// }
