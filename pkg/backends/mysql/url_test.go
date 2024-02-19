package mysql

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {
	conf, _, err := config.Load("trickster", "test",
		[]string{"-origin-url", "none:9090",
			"-provider", "mysql",
			"-log-level", "debug"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	backendClient, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	client := backendClient.(*Client)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	q := url.Values{"query": {tq03}}
	r.URL.RawQuery = q.Encode()

	trq, _, _, err := client.ParseTimeRangeQuery(r)
	if err != nil {
		t.Fatal(err)
	}
	orig := trq.Statement

	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()
	e := timeseries.Extent{Start: start, End: end}

	client.SetExtent(r, trq, &e)
	now := trq.Statement

	// Check everything to make sure values change appropriately
	if trq.Extent != e {
		t.Error(trq.Extent, e)
	}
	if orig == now {
		t.Error(orig, now)
	}
}
