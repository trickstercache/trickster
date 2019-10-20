package irondb

import (
	"io/ioutil"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/errors"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestRollupHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/rollup/00112233-4455-6677-8899-aabbccddeeff/metric"+
		"?start_ts=0&end_ts=900&rollup_span=300s&type=average", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	client.RollupHandler(w, r)
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

func TestRollupHandlerSetExtent(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0//rollup/00112233-4455-6677-8899-aabbccddeeff/metric", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("RollupHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	now := time.Now()
	then := now.Add(-5 * time.Hour)

	// should short circuit from internal checks
	// all though this func does not return a value to test, these exercise all coverage areas
	client.rollupHandlerSetExtent(nil, nil)
	client.rollupHandlerSetExtent(tr, &timeseries.Extent{})
	client.rollupHandlerSetExtent(tr, &timeseries.Extent{Start: then, End: now})
	r.URL.RawQuery = "start_ts=0&end_ts=900&rollup_span=300s&type=average"
	client.rollupHandlerSetExtent(tr, &timeseries.Extent{Start: now, End: now})

}

func TestRollupHandlerParseTimeRangeQuery(t *testing.T) {

	// provide bad URL with no TimeRange query params
	client := &Client{name: "test"}
	hc := tu.NewTestWebClient()
	cfg := config.NewOriginConfig()
	cfg.Paths, _ = client.DefaultPathConfigs(cfg)
	r, err := http.NewRequest(http.MethodGet, "http://0/rollup/00112233-4455-6677-8899-aabbccddeeff/metric", nil)
	if err != nil {
		t.Error(err)
	}
	tr := model.NewRequest("RollupHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	// case where everything is good
	r.URL.RawQuery = "start_ts=0&end_ts=900&rollup_span=300s&type=average"
	trq, err := client.rollupHandlerParseTimeRangeQuery(tr)
	if err != nil {
		t.Error(err)
	}
	if trq == nil {
		t.Errorf("expected value got nil for %s", r.URL.RawQuery)
	}

	// missing start param
	r.URL.RawQuery = "end_ts=3456&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(tr)
	expected := errors.MissingURLParam(upStart)
	if !reflect.DeepEqual(err, expected) {
		t.Errorf("expected %s got %s", expected.Error(), err)
	}

	// can't parse start param
	r.URL.RawQuery = "start_ts=abcd&end_ts=3456&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(tr)
	expectedS := `unable to parse timestamp abcd: strconv.ParseInt: parsing "abcd": invalid syntax`
	if err.Error() != expectedS {
		t.Errorf("expected %s got %s", expectedS, err.Error())
	}

	// missing end param
	r.URL.RawQuery = "start_ts=9012&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(tr)
	expected = errors.MissingURLParam(upEnd)
	if !reflect.DeepEqual(err, expected) {
		t.Errorf("expected %s got %s", expected.Error(), err)
	}

	// can't parse end param
	r.URL.RawQuery = "start_ts=9012&end_ts=efgh&rollup_span=7890"
	_, err = client.rollupHandlerParseTimeRangeQuery(tr)
	expectedS = `unable to parse timestamp efgh: strconv.ParseInt: parsing "efgh": invalid syntax`
	if err.Error() != expectedS {
		t.Errorf("expected %s got %s", expectedS, err.Error())
	}

	// missing rollup_span param
	r.URL.RawQuery = "start_ts=9012&end_ts=3456"
	_, err = client.rollupHandlerParseTimeRangeQuery(tr)
	expected = errors.MissingURLParam(upSpan)
	if !reflect.DeepEqual(err, expected) {
		t.Errorf("expected %s got %s", expected.Error(), err)
	}

	// unparsable rollup_span param
	r.URL.RawQuery = "start_ts=9012&end_ts=3456&rollup_span=pqrs"
	_, err = client.rollupHandlerParseTimeRangeQuery(tr)
	expectedS = `unable to parse duration pqrs: time: invalid duration pqrs`
	if err.Error() != expectedS {
		t.Errorf("expected %s got %s", expectedS, err.Error())
	}

}

func TestRollupHandlerFastForwardURLError(t *testing.T) {

	client := &Client{name: "test"}
	_, _, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs,
		200, "{}", nil, "irondb",
		"/rollup/00112233-4455-6677-8899-aabbccddeeff/metric", "debug")
	if err != nil {
		t.Error(err)
	}
	cfg := tc.OriginConfig(r.Context())
	tr := model.NewRequest("RollupHandler", r.Method, r.URL, r.Header, cfg.Timeout, r, hc)

	_, err = client.rollupHandlerFastForwardURL(tr)
	if err == nil {
		t.Errorf("expected error: %s", "invalid parameters")
	}

}
