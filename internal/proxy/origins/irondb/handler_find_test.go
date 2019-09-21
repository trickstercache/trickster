package irondb

import (
	"io/ioutil"
	"testing"

	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestFindHandler(t *testing.T) {

	client := &Client{name: "test"}
	ts, w, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/find/1/tags?query=metric"+
		"&activity_start_secs=0&activity_end_secs=900", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	_, ok := client.config.Paths["/"+mnFind]
	if !ok {
		t.Errorf("could not find path config named %s", mnFind)
	}

	client.FindHandler(w, r)
	resp := w.Result()

	// It should return 200 OK.
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
