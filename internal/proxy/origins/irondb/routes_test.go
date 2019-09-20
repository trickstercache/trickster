package irondb

import (
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

func TestRegisterHandlers(t *testing.T) {
	c := &Client{}
	c.registerHandlers()
	if _, ok := c.handlers[mnCAQL]; !ok {
		t.Errorf("expected to find handler named: %s", mnCAQL)
	}
}

func TestHandlers(t *testing.T) {
	c := &Client{}
	m := c.Handlers()
	if _, ok := m[mnCAQL]; !ok {
		t.Errorf("expected to find handler named: %s", mnCAQL)
	}
}

func TestDefaultPathConfigs(t *testing.T) {

	err := config.Load("trickster", "test", []string{"-origin-url", "http://127.0.0.1", "-origin-type", "irondb", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	log.Init()
	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	c := &Client{cache: cache}
	dpc, ordered := c.DefaultPathConfigs()

	if _, ok := dpc["/"]; !ok {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 9
	if len(ordered) != expectedLen {
		t.Errorf("expected ordered length to be: %d got %d", expectedLen, len(ordered))
	}

}
