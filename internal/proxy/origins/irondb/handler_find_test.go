package irondb

import (
	"io/ioutil"
	"net/http/httptest"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestFindHandler(t *testing.T) {
	es := tu.NewTestServer(200, "{}")
	defer es.Close()
	err := config.Load("trickster", "test",
		[]string{"-origin", es.URL,
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/find/1/tags?query=metric"+
		"&activity_start_secs=0&activity_end_secs=900", nil)
	client := &Client{
		name:      "default",
		config:    config.Origins["default"],
		cache:     cache,
		webClient: tu.NewTestWebClient(),
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
