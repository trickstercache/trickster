package irondb

import (
	"testing"

	tc "github.com/Comcast/trickster/internal/util/context"
	tu "github.com/Comcast/trickster/internal/util/testing"
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

	client := &Client{name: "test"}
	ts, _, r, hc, err := tu.NewTestInstance("", client.DefaultPathConfigs, 200, "{}", nil, "irondb", "/health", "debug")
	client.config = tc.OriginConfig(r.Context())
	client.webClient = hc
	defer ts.Close()
	if err != nil {
		t.Error(err)
	}

	if _, ok := client.config.Paths["/"]; !ok {
		t.Errorf("expected to find path named: %s", "/")
	}

	const expectedLen = 10
	if len(client.config.Paths) != expectedLen {
		t.Errorf("expected ordered length to be: %d got %d", expectedLen, len(client.config.Paths))
	}

}
