package irondb

import (
	"io/ioutil"
	"net/http"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestCAQLHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/extension/lua/caql_v1"+
		"?query=metric:average(%2200112233-4455-6677-8899-aabbccddeeff%22,"+
		"%22metric%22)&start=0&end=900&period=300", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	client.CAQLHandler(w, r)
	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}

func TestCaqlHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/extension/lua/caql_v1", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("CAQLHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	// should short circuit from internal checks
	// all though this func does not return a value to test, these exercise all coverage areas
	client.caqlHandlerSetExtent(nil, nil)
	client.caqlHandlerSetExtent(tr, &timeseries.Extent{})
	client.caqlHandlerSetExtent(tr, &timeseries.Extent{Start: then, End: now})
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456&period=7890"
	client.caqlHandlerSetExtent(tr, &timeseries.Extent{Start: now, End: now})

}

func TestCaqlHandlerParseTimeRangeQuery(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/extension/lua/caql_v1", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("CAQLHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	// case where everythings good
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456&period=7890"
	trq, err := client.caqlHandlerParseTimeRangeQuery(tr)
	if err != nil {
		t.Error(err)
	}
	if trq == nil {
		t.Errorf("expected value got nil for %s", r.URL.RawQuery)
	}

	// missing q param but query is present
	r.URL.RawQuery = "help=1234&query=5678&start=9012&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err != nil {
		t.Error(err)
		return
	}

	// missing query param but q is present
	r.URL.RawQuery = "q=1234&start=9012&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err != nil {
		t.Error(err)
	}

	// missing query and q params
	r.URL.RawQuery = "start=9012&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// missing start param
	r.URL.RawQuery = "q=1234&query=5678&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// cant parse start param
	r.URL.RawQuery = "q=1234&query=5678&start=abcd&end=3456&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// missing end param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// can't parse end param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=efgh&period=7890"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// missing period param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

	// unparseable period param
	r.URL.RawQuery = "q=1234&query=5678&start=9012&end=3456&period=pqrs"
	_, err = client.caqlHandlerParseTimeRangeQuery(tr)
	if err == nil {
		t.Errorf("expected error for parameter missing")
	}

}

func TestCaqlHandlerFastForwardURLError(t *testing.T) {

	client := &Client{name: "test"}
	_, _, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/extension/lua/caql_v1", "debug")
	if err != nil {
		t.Error(err)
	}
	cfg := tc.OriginConfig(r.Context())
	tr := model.NewRequest("CAQLHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	_, err = client.caqlHandlerFastForwardURL(tr)
	if err == nil {
		t.Errorf("expected error: %s", "invalid parameters")
	}

}
