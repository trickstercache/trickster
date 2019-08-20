package irondb

import (
	"os"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/go-kit/kit/log"
)

var logger log.Logger

func init() {
	logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))
	metrics.Init(logger)
}

func TestNewClient(t *testing.T) {
	err := config.Load("trickster", "test", nil)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig(logger)
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	oc := &config.OriginConfig{Type: "TEST_CLIENT"}
	c := NewClient("default", oc, cache, logger)
	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Type != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Type)
	}

	if c.Configuration().Type != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Type)
	}
}

func TestConfiguration(t *testing.T) {
	oc := &config.OriginConfig{Type: "TEST"}
	client := Client{config: oc}
	c := client.Configuration()
	if c.Type != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c.Type)
	}
}

func TestCache(t *testing.T) {
	err := config.Load("trickster", "test", nil)
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig(logger)
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	client := Client{cache: cache}
	c := client.Cache()
	if c.Configuration().Type != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Configuration().Type)
	}
}

func TestName(t *testing.T) {
	client := Client{name: "TEST"}
	c := client.Name()
	if c != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c)
	}
}

func TestHTTPClient(t *testing.T) {
	oc := &config.OriginConfig{Type: "TEST"}
	client := NewClient("test", oc, nil, logger)
	if client.HTTPClient() == nil {
		t.Errorf("missing http client")
	}
}
