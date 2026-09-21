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

package static

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// seriesOf counts the series a vector exports for a backend, without creating any
func seriesOf(t *testing.T, vec prometheus.Collector, backend string) int {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	go func() {
		vec.Collect(ch)
		close(ch)
	}()
	var n int
	for m := range ch {
		var out dto.Metric
		if err := m.Write(&out); err != nil {
			t.Fatal(err)
		}
		for _, label := range out.GetLabel() {
			if label.GetName() == keys.Backend_Name && label.GetValue() == backend {
				n++
			}
		}
	}
	return n
}

// every vector a static backend publishes to, and how many series it has in each once in service
var seriesVectors = []struct {
	name string
	vec  prometheus.Collector
	n    int
}{
	{"responses", metrics.FileserverResponses, int(numCacheStatuses) * (len(renditions) + 1)},
	{"cache events", metrics.FileserverCacheEvents, 2},
	{"usage objects", metrics.FileserverCacheObjects, 1},
	{"usage bytes", metrics.FileserverCacheBytes, 1},
	{"max objects", metrics.FileserverCacheMaxObjects, 1},
	{"max bytes", metrics.FileserverCacheMaxBytes, 1},
}

func requireSeries(t *testing.T, backend string, published bool, when string) {
	t.Helper()
	for _, v := range seriesVectors {
		want := 0
		if published {
			want = v.n
		}
		if got := seriesOf(t, v.vec, backend); got != want {
			t.Errorf("%s: expected %d %s series for %s, got %d", when, want, v.name, backend, got)
		}
	}
}

func newSeriesTestClient(t *testing.T, name, root string) *Client {
	t.Helper()
	b, err := NewClient(name, testBackendOptions(root), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	t.Cleanup(c.Stop)
	return c
}

func TestSeriesOfARejectedConfigurationAreNeverPublished(t *testing.T) {
	root := newTestSite(t)
	name := testName(t)
	// built to be validated, it serves (as a dry run's routes may be exercised) but is never started
	c := newSeriesTestClient(t, name, root)
	if resp := get(t, c.handler, http.MethodGet, "/"); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected the file, got %d", resp.StatusCode)
	}
	requireSeries(t, name, false, "never started")
	// nor does a server that couldn't be built at all leave anything behind
	if _, err := NewClient(name, testBackendOptions(filepath.Join(root, "missing")), nil, nil, nil, nil); err == nil {
		t.Fatal("expected an error for a missing root")
	}
	requireSeries(t, name, false, "failed to build")
}

func TestSeriesFollowABackendThroughReloads(t *testing.T) {
	root := newTestSite(t)
	name, renamed := testName(t), testName(t)
	hits := func(backend string) float64 {
		return testutil.ToFloat64(metrics.FileserverResponses.WithLabelValues(backend, "hit", identityLabel))
	}
	serve := func(c *Client, n int) {
		for range n {
			get(t, c.handler, http.MethodGet, "/")
			// made away from the request, and this client isn't one the helper knows to wait for
			c.server.stores.Wait()
		}
	}

	first := newSeriesTestClient(t, name, root)
	StartClients(backends.Backends{name: first})
	requireSeries(t, name, true, "in service")
	serve(first, 4)
	if hits(name) != 3 {
		t.Fatalf("expected 3 hits after the first load, got %v", hits(name))
	}

	// a reload that keeps the name: the old is stopped, the new started, and the counters carry on
	second := newSeriesTestClient(t, name, root)
	StopClients(backends.Backends{name: first})
	StartClients(backends.Backends{name: second})
	requireSeries(t, name, true, "reloaded under the same name")
	if hits(name) != 3 {
		t.Errorf("expected the counters to carry over the reload, got %v", hits(name))
	}
	serve(second, 3)
	// a request still draining through the old server counts to the same series
	get(t, first.handler, http.MethodGet, "/nope")
	if hits(name) != 5 {
		t.Errorf("expected the replacement to count on from where the last left off, got %v", hits(name))
	}
	// stopping the old one again, as a rollback's cleanup may, doesn't take the replacement's away
	first.Stop()
	StartClients(backends.Backends{name: second})
	requireSeries(t, name, true, "after the replaced server was stopped again")

	// a reload that renames the backend: the old name's series go, and the new name's appear
	third := newSeriesTestClient(t, renamed, root)
	StopClients(backends.Backends{name: second})
	StartClients(backends.Backends{renamed: third})
	requireSeries(t, name, false, "renamed away")
	requireSeries(t, renamed, true, "renamed to")

	// a reload that removes it, leaving a configuration with no static backends at all
	StopClients(backends.Backends{renamed: third})
	StartClients(backends.Backends{})
	requireSeries(t, renamed, false, "removed")

	// a rollback puts the old clients back into service, and their series with them
	StartClients(backends.Backends{renamed: third})
	requireSeries(t, renamed, true, "rolled back")
}

func TestSeriesOfABackendWithoutACache(t *testing.T) {
	root := newTestSite(t)
	name := testName(t)
	o := testBackendOptions(root)
	o.Static.FileserverCache.Disabled = true
	b, err := NewClient(name, o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := b.(*Client)
	StartClients(backends.Backends{name: c})
	get(t, c.handler, http.MethodGet, "/")
	// it still serves files, which are counted, but has no cache to report on
	if got := seriesOf(t, metrics.FileserverResponses, name); got != seriesVectors[0].n {
		t.Errorf("expected the response series of a backend with no cache, got %d", got)
	}
	for _, v := range seriesVectors[1:] {
		if got := seriesOf(t, v.vec, name); got != 0 {
			t.Errorf("expected no %s series without a cache, got %d", v.name, got)
		}
	}
	StopClients(backends.Backends{name: c})
	StartClients(backends.Backends{})
	requireSeries(t, name, false, "removed")
}
