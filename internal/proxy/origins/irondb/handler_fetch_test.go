package irondb

import (
	"bytes"
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

func TestFetchHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/rollup/00112233-4455-6677-8899-aabbccddeeff/metric"+
		"?start_ts=0&end_ts=900&rollup_span=300s&type=average", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	client.FetchHandler(w, r)
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

func TestFetchHandlerDeriveCacheKey(t *testing.T) {

	client := &Client{name: "test"}
	path := "/fetch/0/900/00112233-4455-6677-8899-aabbccddeeff/metric"
	_, _, r, _, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", path, "debug")
	if err != nil {
		t.Error(err)
	}

	r.Body = ioutil.NopCloser(bytes.NewReader([]byte("{}")))

	const expected = "a34bbb372c505e9eea0e0589e16c0914"
	result := client.fetchHandlerDeriveCacheKey(path, r.URL.Query(), r.Header, r.Body, "extra")
	if result != expected {
		t.Errorf("exected %s got %s", expected, result)
	}

}

func TestFetchHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("FetchHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	r.Body = ioutil.NopCloser(bytes.NewReader([]byte(`{"start": 300, "period": 300, "count": 5}`)))

	// should short circuit from internal checks
	// all though this func does not return a value to test, these exercise all coverage areas
	client.fetchHandlerSetExtent(nil, nil)
	client.fetchHandlerSetExtent(tr, &timeseries.Extent{Start: then, End: now})
	client.fetchHandlerSetExtent(tr, &timeseries.Extent{Start: now, End: now})
	r.Body = ioutil.NopCloser(bytes.NewReader([]byte(`{a}`)))
	client.fetchHandlerSetExtent(tr, &timeseries.Extent{Start: then, End: now})

}

func TestFetchHandlerParseTimeRangeQuery(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	r, err := http.NewRequest(http.MethodGet, "http://0/", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("FetchHandler", r.Method, r.URL, r.Header, time.Second*300, r, hc)

	r.Body = ioutil.NopCloser(bytes.NewReader([]byte(`{"start": 300, "period": 300, "count": 5}`)))
	_, err = client.fetchHandlerParseTimeRangeQuery(tr)
	if err != nil {
		t.Error(err)
	}

	r.Body = ioutil.NopCloser(bytes.NewReader([]byte(`{"period": 300, "count": 5}`)))
	expected := "missing request parameter: start"
	_, err = client.fetchHandlerParseTimeRangeQuery(tr)
	if err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	r.Body = ioutil.NopCloser(bytes.NewReader([]byte(`{"start": 300, "count": 5}`)))
	expected = "missing request parameter: period"
	_, err = client.fetchHandlerParseTimeRangeQuery(tr)
	if err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}

	r.Body = ioutil.NopCloser(bytes.NewReader([]byte(`{"start": 300, "period": 300}`)))
	expected = "missing request parameter: count"
	_, err = client.fetchHandlerParseTimeRangeQuery(tr)
	if err.Error() != expected {
		t.Errorf("expected %s got %s", expected, err.Error())
	}
}
