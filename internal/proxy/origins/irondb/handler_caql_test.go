package irondb

import (
	"io/ioutil"
	"net/http/httptest"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestCAQLHandler(t *testing.T) {
	es := tu.NewTestServer(200, "{}")
	defer es.Close()
	err := config.Load("trickster", "test",
		[]string{"-origin", es.URL,
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig(logger)
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http:/0/extension/lua/caql_v1"+
		"?query=metric:average(%2200112233-4455-6677-8899-aabbccddeeff%22,"+
		"%22metric%22)&start=0&end=900&period=300", nil)
	client := &Client{
		name:      "default",
		config:    config.Origins["default"],
		cache:     cache,
		webClient: tu.NewTestWebClient(),
		logger:    logger,
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
