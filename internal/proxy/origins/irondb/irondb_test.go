package irondb

import (
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func init() {
	// Initialize Trickster instrumentation metrics.
	metrics.Init()
}

func TestNewClient(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://example.com", "-origin-type", "TEST_CLIENT"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	oc := &config.OriginConfig{OriginType: "TEST_CLIENT"}
	c, err := NewClient("default", oc, cache)
	if err != nil {
		t.Error(err)
	}
	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().CacheType != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().CacheType)
	}

	if c.Configuration().OriginType != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().OriginType)
	}
}

func TestConfiguration(t *testing.T) {
	oc := &config.OriginConfig{OriginType: "TEST"}
	client := Client{config: oc}
	c := client.Configuration()
	if c.OriginType != "TEST" {
		t.Errorf("expected %s got %s", "TEST", c.OriginType)
	}
}

func TestCache(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://example.com", "-origin-type", "TEST_CLIENT"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	client := Client{cache: cache}
	c := client.Cache()
	if c.Configuration().CacheType != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Configuration().CacheType)
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
	oc := &config.OriginConfig{OriginType: "TEST"}
	client, err := NewClient("test", oc, nil)
	if err != nil {
		t.Error(err)
	}
	if client.HTTPClient() == nil {
		t.Errorf("missing http client")
	}
}
