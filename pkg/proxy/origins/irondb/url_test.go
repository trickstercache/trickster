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

package irondb

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/proxy/errors"
	po "github.com/tricksterproxy/trickster/pkg/proxy/paths/options"
	"github.com/tricksterproxy/trickster/pkg/proxy/request"
	"github.com/tricksterproxy/trickster/pkg/timeseries"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
)

func TestSetExtent(t *testing.T) {
	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	stFl := time.Unix(start.Unix()-(start.Unix()%300), 0)
	etFl := time.Unix(end.Unix()-(end.Unix()%300), 0)
	e := &timeseries.Extent{Start: start, End: end}
	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090",
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	client := &Client{config: oc}

	client.SetExtent(nil, nil, nil)

	client.makeTrqParsers()
	client.makeExtentSetters()

	pcs := client.DefaultPathConfigs(oc)
	rsc := request.NewResources(oc, nil, nil, nil, client, nil, tl.ConsoleLogger("error"))

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
			expQuery: "end=" + formatTimestamp(etFl, false) +
				"&period=300&query=metric%3Aaverage%28%22" +
				"00112233-4455-6677-8899-aabbccddeeff%22%2C%22metric%22%29" +
				"&start=" + formatTimestamp(stFl, false),
			p: pcs["/extension/lua/caql_v1"],
		},
		{ // case 1
			handler: "HistogramHandler",
			u: &url.URL{
				Path: "/histogram/0/900/300/" +
					"00112233-4455-6677-8899-aabbccddeeff/metric",
				RawQuery: "",
			},
			expPath: "/histogram/" + formatTimestamp(stFl, false) +
				"/" + formatTimestamp(etFl, false) + "/300" +
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
			expQuery: "end_ts=" + formatTimestamp(end, true) +
				"&start_ts=" + formatTimestamp(start, true),
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
			expQuery: "end_ts=" + formatTimestamp(etFl, true) +
				"&rollup_span=300s" + "&start_ts=" +
				formatTimestamp(stFl, true) + "&type=count",
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
			expPath: "/read/" + formatTimestamp(start, false) +
				"/" + formatTimestamp(end, false) +
				"/00112233-4455-6677-8899-aabbccddeeff/metric",
			expQuery: "",
			p:        pcs["/read/"],
		},
	}

	for i, c := range cases {

		t.Run(strconv.Itoa(i), func(t *testing.T) {

			r, _ := http.NewRequest(http.MethodGet, c.u.String(), ioutil.NopCloser(bytes.NewBufferString(c.body)))
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
				b, err := ioutil.ReadAll(r.Body)
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
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	oc := conf.Origins["default"]
	client := &Client{config: oc}

	_, err = client.FastForwardURL(nil)
	if err == nil {
		t.Error("expected error")
	}

	client.makeTrqParsers()
	client.makeExtentSetters()

	pcs := client.DefaultPathConfigs(oc)

	rsc := request.NewResources(oc, nil, nil, nil, client, nil, tl.ConsoleLogger("error"))

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
				"?end=" + formatTimestamp(time.Unix(end, 0), false) +
				"&period=300&query=metric%3Aaverage%28%22" +
				"00112233-4455-6677-8899-aabbccddeeff%22%2C%22metric%22%29" +
				"&start=" + formatTimestamp(time.Unix(start, 0), false),
			p: pcs["/extension/lua/caql_v1"],
		},
		{ // case 1
			handler: "HistogramHandler",
			u: &url.URL{
				Path: "/histogram/0/900/300/" +
					"00112233-4455-6677-8899-aabbccddeeff/metric",
				RawQuery: "",
			},
			exp: "/histogram/" + formatTimestamp(time.Unix(start, 0), false) +
				"/" + formatTimestamp(time.Unix(end, 0), false) +
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
				"?end_ts=" + formatTimestamp(time.Unix(end, 0), true) +
				"&rollup_span=300s" +
				"&start_ts=" + formatTimestamp(time.Unix(start, 0), true) +
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
			rsc.PathConfig = c.p
			r = request.SetResources(r, rsc)
			u, err := client.FastForwardURL(r)
			if c.handler != "ProxyHandler" && err != nil {
				t.Error(err)
			}

			if c.handler == "ProxyHandler" && err.Error() != "unknown handler name: ProxyHandler" {
				t.Errorf("expected error: %s", "unknown handler name")
			}

			if u != nil {
				if u.String() != c.exp {
					t.Errorf("Expected URL: %v, got: %v", c.exp, u.String())
				}
			}
		})
	}
}

func TestFormatTimestamp(t *testing.T) {
	tm := time.Unix(123456789, int64(time.Millisecond))
	exp := "123456789.001"
	res := formatTimestamp(tm, true)
	if res != exp {
		t.Errorf("Expected string: %v, got: %v", exp, res)
	}

	tm = time.Unix(123456789, int64(time.Millisecond))
	exp = "123456789"
	res = formatTimestamp(tm, false)
	if res != exp {
		t.Errorf("Expected string: %v, got: %v", exp, res)
	}
}

func TestParseTimestamp(t *testing.T) {
	v := "123456789.001"
	res, err := parseTimestamp(v)
	if err != nil {
		t.Fatalf("Error parsing %s: %v", v, err.Error())
	}

	exp := time.Unix(123456789, int64(time.Millisecond))
	if !res.Equal(exp) {
		t.Errorf("Expected time: %v, got: %v", exp, res)
	}

	v = "1.a"
	_, err = parseTimestamp(v)
	if err == nil {
		t.Fatalf("expected error: %s", "parse timestamp")
	}

}

func TestParseTimerangeQuery(t *testing.T) {
	expected := errors.ErrNotTimeRangeQuery
	client := &Client{name: "test"}
	r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)

	r = request.SetResources(r, request.NewResources(client.config, &po.Options{},
		nil, nil, client, nil, tl.ConsoleLogger("error")))

	_, err := client.ParseTimeRangeQuery(r)
	if err == nil || err != expected {
		t.Errorf("expected %s got %v", expected.Error(), err.Error())
	}
}
