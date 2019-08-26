package irondb

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestDeriveCacheKey(t *testing.T) {
	cases := []struct {
		handler string
		method  string
		u       *url.URL
		body    string
		exp     string
	}{
		{
			handler: "CAQLHandler",
			method:  "GET",
			u: &url.URL{
				Path: "/extension/lua/caql_v1",
				RawQuery: "query=metric:average(%22" +
					"00112233-4455-6677-8899-aabbccddeeff%22," +
					"%22metric%22)&start=0&end=900&period=300",
			},
			exp: "03c35fa30d7865780af207af433bc7dc",
		},
		{
			handler: "FindHandler",
			method:  "GET",
			u: &url.URL{
				Path: "/find/1/tags",
				RawQuery: "?query=metric" +
					"&activity_start_secs=0&activity_end_secs=900",
			},
			exp: "fb545b9f5aaae0a45531864714748e26",
		},
		{
			handler: "HistogramHandler",
			method:  "GET",
			u: &url.URL{
				Path: "/histogram/0/900/300/" +
					"00112233-4455-6677-8899-aabbccddeeff/metric",
				RawQuery: "",
			},
			exp: "98d5c9762b841b995307612fda7dcac4",
		},
		{
			handler: "RawHandler",
			method:  "GET",
			u: &url.URL{
				Path:     "/raw/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
				RawQuery: "start_ts=1560902400.000&end_ts=1561055856.000",
			},
			exp: "6c17f668f321c16fa051fa2c0fd65889",
		},
		{
			handler: "RollupHandler",
			method:  "GET",
			u: &url.URL{
				Path: "/rollup/77b69b37-5d52-4c48-8ed2-ed61d09a85d9/test",
				RawQuery: "start_ts=1561030000&end_ts=1561036114" +
					"&rollup_span=1s&type=average",
			},
			exp: "eeec5d29807c9112b906f840389273a9",
		},
		{
			handler: "FetchHandler",
			method:  "POST",
			u: &url.URL{
				Path: "/fetch",
			},
			body: `{
				"start":1555616700,
				"period":60,
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
			exp: "4752204d4649b00c2b60172849800719",
		},
		{
			handler: "TextHandler",
			method:  "GET",
			u: &url.URL{
				Path: "/read/0/900/00112233-4455-6677-8899-aabbccddeeff" +
					"/metric",
				RawQuery: "",
			},
			exp: "a506d1700414b1d0ac15340bd619fdab",
		},
		{
			handler: "ProxyHandler",
			method:  "GET",
			u: &url.URL{
				Path:     "/test",
				RawQuery: "",
			},
			exp: "d41d8cd98f00b204e9800998ecf8427e",
		},
	}

	client := &Client{}
	for _, c := range cases {
		r := &model.Request{
			HandlerName: c.handler,
			HTTPMethod:  c.method,
			URL:         c.u,
			TemplateURL: c.u,
			ClientRequest: &http.Request{
				Body: ioutil.NopCloser(bytes.NewBufferString(c.body)),
			},
			TimeRangeQuery: &timeseries.TimeRangeQuery{
				Step: time.Duration(60) * time.Second,
			},
		}

		key := client.DeriveCacheKey(r, "extra")
		if key != c.exp {
			t.Errorf("Expected key: %s, got: %s", c.exp, key)
		}
	}
}
