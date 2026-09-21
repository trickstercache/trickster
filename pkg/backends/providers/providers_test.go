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

package providers

import (
	"slices"
	"strconv"
	"testing"
)

func TestProviderString(t *testing.T) {
	t1 := RPCID
	t2 := PrometheusID
	var t3 Provider = 13

	if t1.String() != ReverseProxyCacheShort {
		t.Errorf("expected %s got %s", ReverseProxyCacheShort, t1.String())
	}

	if t2.String() != Prometheus {
		t.Errorf("expected %s got %s", Prometheus, t2.String())
	}

	if t3.String() != "13" {
		t.Errorf("expected %s got %s", "13", t3.String())
	}
}

func TestIsValidProvider(t *testing.T) {
	tests := []struct {
		o        string
		expected bool
	}{
		{ReverseProxyCacheShort, true},
		{Prometheus, true},
		{"", false},
		{"invalid", false},
		{InfluxDB, true},
		{Graphite, true},
		{Druid, true},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			res := IsValidProvider(test.o)
			if test.expected != res {
				t.Errorf("expected %t got %t", test.expected, res)
			}
		})
	}
}

func TestIsSupportedTimeSeriesProvider(t *testing.T) {
	name := "test-should-fail"
	ok := IsSupportedTimeSeriesProvider(name)
	if ok {
		t.Error("expected false")
	}

	name = Prometheus
	ok = IsSupportedTimeSeriesProvider(name)
	if !ok {
		t.Error("expected true")
	}

	name = Graphite
	ok = IsSupportedTimeSeriesProvider(name)
	if !ok {
		t.Error("expected true")
	}
	if IsSupportedTimeSeriesMergeProvider(name) {
		t.Error("expected false")
	}
	if GraphiteID.String() != Graphite {
		t.Errorf("expected %s got %s", Graphite, GraphiteID.String())
	}
	if !IsSupportedTimeSeriesProvider(Druid) {
		t.Error("expected Druid to be a supported time series provider")
	}
	if DruidID.String() != Druid {
		t.Errorf("expected %s got %s", Druid, DruidID.String())
	}
}

func TestStaticProvider(t *testing.T) {
	if !IsValidProvider(Static) || StaticID.String() != Static {
		t.Errorf("expected %s to be a valid provider", Static)
	}
	if !NonCacheBackends().Contains(Static) || !NonOriginBackends().Contains(Static) {
		t.Error("expected static to need neither a cache nor an origin")
	}
	if IsSupportedTimeSeriesProvider(Static) {
		t.Error("expected false")
	}
}

func TestMySQLUsesCache(t *testing.T) {
	if NonCacheBackends().Contains(MySQL) {
		t.Fatal("MySQL must be initialized and validated with a cache")
	}
}

func TestHTTPTimeSeriesProviderNames(t *testing.T) {
	names := HTTPTimeSeriesProviderNames()
	if len(names) != len(supportedHTTPTimeSeries) {
		t.Fatalf("names = %v", names)
	}
	if !slices.IsSorted(names) {
		t.Fatalf("names are not sorted: %v", names)
	}
	for _, n := range names {
		if !IsSupportedTimeSeriesProvider(n) || !IsSupportedHTTPTimeSeriesProvider(n) {
			t.Fatalf("%q is not a supported http time series provider", n)
		}
	}
	if !IsSupportedHTTPTimeSeriesProvider(Druid) {
		t.Fatal("druid is not registered as an http time series provider")
	}
	if IsSupportedHTTPTimeSeriesProvider(MySQL) || IsSupportedHTTPTimeSeriesProvider(ReverseProxy) {
		t.Fatal("mysql and the reverse proxy are not http time series providers")
	}
}
