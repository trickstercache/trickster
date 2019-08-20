package irondb

import (
	"io/ioutil"
	"net/http/httptest"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestProxyHandler(t *testing.T) {
	es := tu.NewTestServer(200, "test")
	defer es.Close()
	err := config.Load("trickster", "test",
		[]string{"-origin", es.URL,
			"-origin-type", "irondb"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	client := &Client{
		name:      "default",
		config:    config.Origins["default"],
		webClient: tu.NewTestWebClient(),
		logger:    logger,
	}

	client.ProxyHandler(w, r)
	resp := w.Result()

	// It should return 200 OK.
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "test" {
		t.Errorf("expected '{}' got %s.", bodyBytes)
	}
}
