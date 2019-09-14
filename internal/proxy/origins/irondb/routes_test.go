package irondb

import (
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/routing"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestRegisterRoutesNoDefault(t *testing.T) {
	routing.Router = mux.NewRouter()
	es := tu.NewTestServer(200, "{}")
	defer es.Close()
	err := config.Load("trickster", "test",
		[]string{"-origin-url", es.URL,
			"-origin-type", "prometheus",
			"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := config.Origins["default"]
	oc.IsDefault = false
	client := Client{config: oc}
	client.RegisterRoutes("test_default", oc)

	// This should be false.
	r := httptest.NewRequest("GET", "http://0/health", nil)
	rm := &mux.RouteMatch{}
	if routing.Router.Match(r, rm) {
		t.Errorf("unexpected route match")
		return
	}

	// This should be true.
	r = httptest.NewRequest("GET", "http://0/test_default/health", nil)
	if !routing.Router.Match(r, rm) {
		t.Errorf("could not match route")
		return
	}
}

func TestRegisterRoutesDefault(t *testing.T) {
	routing.Router = mux.NewRouter()
	es := tu.NewTestServer(200, "{}")
	defer es.Close()
	err := config.Load("trickster", "test",
		[]string{"-origin-url", es.URL,
			"-origin-type", "irondb",
			"-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	oc := config.Origins["default"]
	client := Client{config: oc}
	client.RegisterRoutes("default", oc)

	// This should be false.
	r := httptest.NewRequest("GET", "http://0/health", nil)
	rm := &mux.RouteMatch{}
	if !routing.Router.Match(r, rm) {
		t.Errorf("could not match route")
		return
	}

	// This should be true.
	r = httptest.NewRequest("GET", "http://0/default/health", nil)
	if !routing.Router.Match(r, rm) {
		t.Errorf("could not match route")
		return
	}
}
