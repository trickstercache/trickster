/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package clickhouse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"

	"github.com/prometheus/common/sigv4"
)

func TestClickhouseClientInterfacing(t *testing.T) {
	// this test ensures the client will properly conform to the
	// Client and TimeseriesBackend interfaces

	c, err := backends.NewTimeseriesBackend("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	var oc backends.Backend = c
	var tc backends.TimeseriesBackend = c

	if oc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", oc.Name())
	}

	if tc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", tc.Name())
	}
}

func TestNewClient(t *testing.T) {
	conf, err := config.Load([]string{"-provider", providers.ClickHouse, "-origin-url", "http://1"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	o := &bo.Options{Provider: "TEST_CLIENT"}
	c, err := NewClient("default", o, nil, cache, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Provider != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Provider)
	}

	if c.Configuration().Provider != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Provider)
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{
		URL: &url.URL{
			Scheme:   "https",
			Host:     "blah.com",
			Path:     "/",
			RawQuery: testRawQuery(),
		},
		Header: http.Header{},
	}
	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if res.Step.Seconds() != 60 {
			t.Errorf("expected 60 got %f", res.Step.Seconds())
		}
		want := 6*time.Hour - time.Minute
		if res.Extent.End.Sub(res.Extent.Start) != want {
			t.Errorf("expected %s got %s", want, res.Extent.End.Sub(res.Extent.Start))
		}
	}

	req.URL.RawQuery = ""
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf("expected error for: %s", "missing URL parameter: [query]")
	}

	req.URL.RawQuery = url.Values(map[string][]string{"query": {
		`SELECT (intDiv(toUInt32(abc), 6z0) * 6z0) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
			`FROM testdb.test_table WHERE abc BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
			`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
			`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`,
	}}).Encode()
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf("expected error for: %s", "not a time range query")
	}

	req.URL.RawQuery = url.Values(map[string][]string{"query": {
		`SELECT (intDiv(toUInt32(0^^^), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
			`FROM testdb.test_table WHERE 0^^^ BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
			`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
			`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`,
	}}).Encode()
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err == nil {
		t.Errorf("expected error for: %s", "not a time range query")
	}
}

func TestNativeListenerAdapterLifecycle(t *testing.T) {
	a := nativeListenerAdapter{}
	o := bo.New()
	o.Provider = providers.ClickHouse
	o.ListenerNames = []string{"default", "native"}
	c := &config.Config{Backends: bo.Lookup{"ch": o}, Listeners: listenerconfig.Lookup{"native": listenerconfig.New("native")}}
	before, err := a.Describe(c, "native")
	if err != nil {
		t.Fatal(err)
	}
	o.ListenerNames = []string{"native", "default", "other"}
	rebound, err := a.Describe(c, "native")
	if err != nil || rebound.RestartKey != before.RestartKey {
		t.Fatalf("listener bindings changed restart identity: %v", err)
	}
	o.ListenerNames = []string{"default", "native"}
	o.OriginURL = "http://localhost:9000"
	after, err := a.Describe(c, "native")
	if err != nil || before.RestartKey == after.RestartKey {
		t.Fatalf("restart identity did not track origin: %v", err)
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusAccepted) })
	client, err := backends.NewTimeseriesBackend("ch", o, nil, h, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := native.BuildRequest{Config: c, ListenerName: "native", Listener: listenerconfig.New("native"), BackendClients: backends.Backends{"ch": client}}
	handler, err := a.Handler(req)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("handler status %d", rec.Code)
	}
	srv, err := a.Build(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	o.RequireTLS = true
	if _, err := a.Describe(c, "native"); err == nil {
		t.Fatal("accepted require_tls without certificates")
	}
	o.RequireTLS = false
	c.Backends["other"] = o.Clone()
	if _, err := a.Describe(c, "native"); err == nil {
		t.Fatal("accepted multiple backend mappings")
	}
}

func TestNativeListenerAdapterValidation(t *testing.T) {
	a := nativeListenerAdapter{}
	if NativeListenerAdapter().Protocol() != listenerconfig.ProtocolClickHouse {
		t.Fatal("exported adapter has wrong protocol")
	}
	if a.Protocol() != listenerconfig.ProtocolClickHouse || !a.SupportsHTTP() || a.Configured(nil) {
		t.Fatal("unexpected ClickHouse adapter capabilities")
	}
	if err := a.ValidateListener(nil); err == nil {
		t.Fatal("accepted nil listener options")
	}
	if err := a.ValidateListener(listenerconfig.New("native")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		o    *bo.Options
	}{
		{"nil", nil},
		{"unsupported protocol", &bo.Options{Protocol: "tcp"}},
		{"native SigV4", &bo.Options{Protocol: "native", SigV4: &sigv4.SigV4Config{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := a.ValidateBackend(test.o); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if err := a.ValidateBackend(&bo.Options{Protocol: "HTTP"}); err != nil {
		t.Fatal(err)
	}
	if err := a.ValidateUserRouter(nil, "", nil); err == nil {
		t.Fatal("accepted native user routing")
	}
	if resolver := a.RouteResolver(native.BuildRequest{}); resolver != nil {
		t.Fatal("unexpected native route resolver")
	}
	if _, err := a.Build(native.BuildRequest{}); err == nil {
		t.Fatal("accepted empty build request")
	}

	if _, _, err := nativeBackend(nil, "native"); err == nil {
		t.Fatal("accepted nil config")
	}
	conf := &config.Config{Backends: bo.Lookup{}}
	if _, err := a.Describe(conf, "native"); err == nil {
		t.Fatal("accepted listener without backend")
	}
	conf.Backends["prom"] = &bo.Options{
		Provider: providers.Prometheus, ListenerNames: []string{"native"},
	}
	if _, err := a.Describe(conf, "native"); err == nil {
		t.Fatal("accepted non-ClickHouse native backend")
	}
	conf.Backends["prom"].Provider = providers.ClickHouse
	if _, err := a.Handler(native.BuildRequest{
		Config: conf, ListenerName: "native", BackendClients: backends.Backends{},
	}); err == nil {
		t.Fatal("accepted missing backend router")
	}
}
