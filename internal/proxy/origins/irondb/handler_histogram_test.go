package irondb

import (
	"io/ioutil"
	"net/http/httptest"
	"testing"

	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestHistogramHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/histogram/0/900/300/00112233-4455-6677-8899-aabbccddeeff/"+
		"metric", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	client.HistogramHandler(w, r)
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

	oc := tc.OriginConfig(r.Context())
	cc := tc.CacheClient(r.Context())
	pc := tc.PathConfig(r.Context())

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET",
		"http://0/irondb/histogram/0/900/300/"+
			"00112233-4455-6677-8899-aabbccddeeff/"+
			"metric", nil)
	r = r.WithContext(tc.WithConfigs(r.Context(), oc, cc, pc))

	client.HistogramHandler(w, r)
	resp = w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "{}" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}
