package irondb

import (
	"net/http/httptest"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestHealthHandler(t *testing.T) {
	es := tu.NewTestServer(200, "")
	defer es.Close()
	err := config.Load("trickster", "test",
		[]string{"-origin", es.URL, "-origin-type", "irondb"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://0/health", nil)
	client := &Client{
		name:      "default",
		config:    config.Origins["default"],
		webClient: tu.NewTestWebClient(),
	}

	client.HealthHandler(w, r)
	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Errorf("Expected status: 200 got %d.", resp.StatusCode)
	}
}
