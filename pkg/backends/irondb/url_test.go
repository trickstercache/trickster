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

package irondb

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/backends/irondb/common"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {
	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	stFl := time.Unix(start.Unix()-(start.Unix()%300), 0)
	etFl := time.Unix(end.Unix()-(end.Unix()%300), 0)
	e := &timeseries.Extent{Start: start, End: end}
	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090",
			"-provider", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)

	client.SetExtent(nil, nil, nil)

	client.makeTrqParsers()
	client.makeExtentSetters()

	pcs := client.DefaultPathConfigs(o)
	rsc := request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error"))

	cases := []struct {
		handler  string
		u        *url.URL
		body     string
		expPath  string
		expQuery string
		expBody  string
		p        *po.Options
	}{
		{ // case 0
			handler: "CAQLHandler",
			u: &url.URL{
				Path: "/extension/lua/caql_v1",
				RawQuery: "query=metric:average(%22" +
					"00112233-4455-6677-8899-aabbccddeeff%22," +
					"%22metric%22)&start=0&end=900&period=300",
			},
			expPath: "/extension/lua/caql_v1",
			expQuery: "end=" + common.FormatTimestamp(etFl, false) +
				"&period=300&query=metric%3Aaverage%28%22" +
				"00112233-4455-6677-8899-aabbccddeeff%22%2C%22metric%22%29" +
				"&start=" + common.FormatTimestamp(stFl, false),
			p: pcs["/extension/lua/caql_v1"],
		},
		{ // case 1
			handler: "HistogramHandler",
			u: &url.URL{
				Path: "/histogram/0/900/300/" +
					"00112233-4455-6677-8899-aabbccddeeff/metric",
				RawQuery: "",
			},
			expPath: "/histogram/" + common.FormatTimestamp(stFl, false) +
				"/" + common.FormatTimestamp(etFl, false) + "/300" +
				"/00112233-4455-6677-8899-aabbccddeeff/metric",
			expQuery: "",
			p:        pcs["/histogram/"],
		},
		{ // case 2
			handler: "RawHandler",
			u: &url.URL{
				Path:     "/raw/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
				RawQuery: "start_ts=1560902400.000&end_ts=1561055856.000",
			},
			expPath: "/raw/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
			expQuery: "end_ts=" + common.FormatTimestamp(end, true) +
				"&start_ts=" + common.FormatTimestamp(start, true),
			p: pcs["/raw/"],
		},
		{ // case 3
			handler: "RollupHandler",
			u: &url.URL{
				Path: "/rollup/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
				RawQuery: "start_ts=1560902400.000&end_ts=1561055856.000" +
					"&rollup_span=300s&type=count",
			},
			expPath: "/rollup/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
			expQuery: "end_ts=" + common.FormatTimestamp(etFl, true) +
				"&rollup_span=300s" + "&start_ts=" +
				common.FormatTimestamp(stFl, true) + "&type=count",
			p: pcs["/rollup/"],
		},
		{ // case 4
			handler: "FetchHandler",
			u: &url.URL{
				Path: "/fetch",
			},
			body: `{
				"start":` + strconv.FormatInt(start.Unix(), 10) + `,
				"period":300,
				"count":10,
				"streams":[
				  {
					"uuid":"00112233-4455-6677-8899-aabbccddeeff",
					"name":"test",
					"kind":"numeric",
					"transform": "average"
				  }
				],
				"reduce":[{"label":"test","method":"average"}]
			}`,
			expPath:  "/fetch",
			expQuery: "",
			expBody: `{"count":72,"period":300,"reduce":[{"label":"test",` +
				`"method":"average"}],"start":` +
				strconv.FormatInt(stFl.Unix(), 10) + `,"streams":[{"kind":` +
				`"numeric","name":"test","transform":"average","uuid":` +
				`"00112233-4455-6677-8899-aabbccddeeff"}]}`,
			p: pcs["/fetch"],
		},
		{ // case 5
			handler: "TextHandler",
			u: &url.URL{
				Path: "/read/0/900/00112233-4455-6677-8899-aabbccddeeff" +
					"/metric",
				RawQuery: "",
			},
			expPath: "/read/" + common.FormatTimestamp(start, false) +
				"/" + common.FormatTimestamp(end, false) +
				"/00112233-4455-6677-8899-aabbccddeeff/metric",
			expQuery: "",
			p:        pcs["/read/"],
		},
	}

	for i, c := range cases {

		t.Run(strconv.Itoa(i), func(t *testing.T) {

			r, _ := http.NewRequest(http.MethodGet, c.u.String(), io.NopCloser(bytes.NewBufferString(c.body)))
			rsc.PathConfig = c.p
			r = request.SetResources(r, rsc)

			client.SetExtent(r, nil, e)
			if r.URL.Path != c.expPath {
				t.Errorf("Expected path: %s, got: %s", c.expPath, r.URL.Path)
			}

			if r.URL.RawQuery != c.expQuery {
				t.Errorf("Expected query: %s, got: %s", c.expQuery, r.URL.RawQuery)
			}

			if c.expBody != "" {
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("Unable to read request body: %v", err)
					return
				}

				if string(b) != (c.expBody + "\n") {
					t.Errorf("Expected request body: %v, got: %v", c.expBody,
						string(b))
				}
			}
		})
	}
}

func TestFastForwardURL(t *testing.T) {
	now := time.Now().Unix()
	start := now - (now % 300)
	end := start + 300
	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090",
			"-provider", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	backendClient, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)

	_, err = client.FastForwardRequest(nil)
	if err == nil {
		t.Error("expected error")
	}

	client.makeTrqParsers()
	client.makeExtentSetters()

	pcs := client.DefaultPathConfigs(o)

	rsc := request.NewResources(o, nil, nil, nil, client, nil, tl.ConsoleLogger("error"))

	cases := []struct {
		handler string
		u       *url.URL
		exp     string
		p       *po.Options
	}{
		{ // case 0
			handler: "CAQLHandler",
			u: &url.URL{
				Path: "/extension/lua/caql_v1",
				RawQuery: "query=metric:average(%22" +
					"00112233-4455-6677-8899-aabbccddeeff%22," +
					"%22metric%22)&start=0&end=900&period=300",
			},
			exp: "/extension/lua/caql_v1" +
				"?end=" + common.FormatTimestamp(time.Unix(end, 0), false) +
				"&period=300&query=metric%3Aaverage%28%22" +
				"00112233-4455-6677-8899-aabbccddeeff%22%2C%22metric%22%29" +
				"&start=" + common.FormatTimestamp(time.Unix(start, 0), false),
			p: pcs["/extension/lua/caql_v1"],
		},
		{ // case 1
			handler: "HistogramHandler",
			u: &url.URL{
				Path: "/histogram/0/900/300/" +
					"00112233-4455-6677-8899-aabbccddeeff/metric",
				RawQuery: "",
			},
			exp: "/histogram/" + common.FormatTimestamp(time.Unix(start, 0), false) +
				"/" + common.FormatTimestamp(time.Unix(end, 0), false) +
				"/300" +
				"/00112233-4455-6677-8899-aabbccddeeff/metric",
			p: pcs["/histogram/"],
		},
		{ // case 2
			handler: "RollupHandler",
			u: &url.URL{
				Path: "/rollup/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
				RawQuery: "start_ts=1560902400.000&end_ts=1560903000.000" +
					"&rollup_span=300s&type=count",
			},
			exp: "/rollup/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests" +
				"?end_ts=" + common.FormatTimestamp(time.Unix(end, 0), true) +
				"&rollup_span=300s" +
				"&start_ts=" + common.FormatTimestamp(time.Unix(start, 0), true) +
				"&type=count",
			p: pcs["/rollup/"],
		},
		{ // case 3
			handler: "ProxyHandler",
			u: &url.URL{
				Path:     "/test",
				RawQuery: "",
			},
			exp: "/test",
			p:   pcs["/"],
		},
	}

	for i, c := range cases {
		t.Run(strconv.Itoa(i), func(t *testing.T) {

			r, _ := http.NewRequest(http.MethodGet, c.u.String(), nil)
			r = r.WithContext(context.Background())
			rsc.PathConfig = c.p
			nr := request.SetResources(r, rsc)
			fr, err := client.FastForwardRequest(nr)
			if c.handler != "ProxyHandler" && err != nil {
				t.Error(err)
			}

			if c.handler == "ProxyHandler" && err.Error() != "unknown handler name: ProxyHandler" {
				t.Errorf("expected error: %s", "unknown handler name")
			}

			if fr != nil && fr.URL != nil {
				if fr.URL.String() != c.exp {
					t.Errorf("Expected URL: %v, got: %v", c.exp, fr.URL.String())
				}
			}
		})
	}
}

func TestParseTimerangeQuery(t *testing.T) {
	expected := errors.ErrNotTimeRangeQuery
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)

	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)

	r = request.SetResources(r, request.NewResources(client.Configuration(), &po.Options{},
		nil, nil, client, nil, tl.ConsoleLogger("error")))

	_, _, _, err = client.ParseTimeRangeQuery(r)
	if err == nil || err != expected {
		t.Errorf("expected %s got %v", expected.Error(), err.Error())
	}
}
