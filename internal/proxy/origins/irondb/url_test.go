package irondb

import (
	"bytes"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestSetExtent(t *testing.T) {
	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	stFl := time.Unix(start.Unix()-(start.Unix()%300), 0)
	etFl := time.Unix(end.Unix()-(end.Unix()%300), 0)
	e := &timeseries.Extent{Start: start, End: end}
	err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090",
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := config.Origins["default"]
	client := Client{config: oc}

	cases := []struct {
		handler  string
		u        *url.URL
		body     string
		expPath  string
		expQuery string
		expBody  string
	}{
		{
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
		},
		{
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
		},
		{
			handler: "RawHandler",
			u: &url.URL{
				Path:     "/raw/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
				RawQuery: "start_ts=1560902400.000&end_ts=1561055856.000",
			},
			expPath: "/raw/e312a0cb-dbe9-445d-8346-13b0ae6a3382/requests",
			expQuery: "end_ts=" + formatTimestamp(end, true) +
				"&start_ts=" + formatTimestamp(start, true),
		},
		{
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
		},
		{
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
		},
		{
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
		},
	}

	for _, c := range cases {
		r := &model.Request{
			HandlerName: c.handler,
			URL:         c.u,
			TemplateURL: c.u,
			ClientRequest: &http.Request{
				Body: ioutil.NopCloser(bytes.NewBufferString(c.body)),
			},
		}

		client.SetExtent(r, e)
		if r.URL.Path != c.expPath {
			t.Errorf("Expected path: %s, got: %s", c.expPath, r.URL.Path)
		}

		if r.URL.RawQuery != c.expQuery {
			t.Errorf("Expected query: %s, got: %s", c.expQuery, r.URL.RawQuery)
		}

		if c.expBody != "" {
			b, err := ioutil.ReadAll(r.ClientRequest.Body)
			if err != nil {
				t.Errorf("Unable to read request body: %v", err)
				return
			}

			if string(b) != (c.expBody + "\n") {
				t.Errorf("Expected request body: %v, got: %v", c.expBody,
					string(b))
			}
		}
	}
}

func TestFastForwardURL(t *testing.T) {
	now := time.Now().Unix()
	start := now - (now % 300)
	end := start + 300
	err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090",
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := config.Origins["default"]
	client := Client{config: oc}
	cases := []struct {
		handler string
		u       *url.URL
		exp     string
	}{
		{
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
		},
		{
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
		},
		{
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
		},
		{
			handler: "ProxyHandler",
			u: &url.URL{
				Path:     "/test",
				RawQuery: "",
			},
			exp: "/test",
		},
	}

	for _, c := range cases {
		r := &model.Request{HandlerName: c.handler, URL: c.u}
		u, err := client.FastForwardURL(r)
		if err != nil {
			t.Error(err)
		}

		if u.String() != c.exp {
			t.Errorf("Expected URL: %v, got: %v", c.exp, u.String())
		}
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
}
