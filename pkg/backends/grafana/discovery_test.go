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

package grafana

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestDiscoveryIdentityIsStableAndCredentialScoped(t *testing.T) {
	first := make(http.Header)
	first.Add("Cookie", "session=a")
	first.Add("X-Custom-Auth", "two")
	first.Add("X-Custom-Auth", "one")
	second := make(http.Header)
	second.Add("X-Custom-Auth", "one")
	second.Add("X-Custom-Auth", "two")
	second.Add("Cookie", "session=a")
	if got, want := discoveryIdentity(first), discoveryIdentity(second); got != want {
		t.Fatalf("equivalent headers produced different identities: %q != %q", got, want)
	}
	second.Set("Cookie", "session=b")
	if discoveryIdentity(first) == discoveryIdentity(second) {
		t.Fatal("different Grafana sessions produced the same discovery identity")
	}
}

func TestDiscoveryHeadersApplyConfiguredIdentityHeaders(t *testing.T) {
	client, cache := newGrafanaTestClient(t, "http://127.0.0.1")
	defer cache.Close()

	pathConfig := client.Configuration().Paths[0]
	pathConfig.CacheKeyHeaders = []string{"X-Custom-User"}
	pathConfig.RequestHeaders = map[string]string{
		"-Cookie": "removed",
		"+X-Role": "editor",
	}
	r := httptest.NewRequest(http.MethodGet, "http://trickster.example/", nil)
	r.Header.Set("Cookie", "grafana_session=user-a")
	r.Header.Set("X-Custom-User", "alice")
	rsc := request.NewResources(client.Configuration(), pathConfig,
		cache.Configuration(), cache, client, nil)
	r = request.SetResources(r, rsc)

	headers := client.discoveryHeaders(r)
	if got := headers.Get("Cookie"); got != "" {
		t.Fatalf("Cookie = %q, want configured removal", got)
	}
	if got := headers.Get("X-Custom-User"); got != "alice" {
		t.Fatalf("X-Custom-User = %q, want alice", got)
	}
	if got := headers.Get("X-Role"); got != "editor" {
		t.Fatalf("X-Role = %q, want editor", got)
	}
}

func TestResolveDataSourceSingleflightsNumericLookup(t *testing.T) {
	var requests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/datasources/7" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, `{"id":7,"uid":"prom-seven","orgId":2,"type":"prometheus","access":"proxy"}`)
	}))
	defer origin.Close()

	client, cache := newGrafanaTestClient(t, origin.URL)
	defer cache.Close()
	headers := make(http.Header)
	headers.Set("Cookie", "grafana_session=user-a")
	ref := dataSourceRef{kind: dataSourceRefID, value: "7"}

	const callers = 20
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ds, err := client.resolveDataSource(context.Background(), ref, headers)
			if err == nil && (ds.ID != 7 || ds.UID != "prom-seven") {
				err = fmt.Errorf("unexpected data source: %#v", ds)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("metadata requests = %d, want 1", got)
	}

	uidRef := dataSourceRef{kind: dataSourceRefUID, value: "prom-seven"}
	if _, err := client.resolveDataSource(context.Background(), uidRef, headers); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("UID lookup did not reuse numeric metadata; requests = %d", got)
	}
}

func TestRegisterHandlersPreloadsDataSourcesWithConfiguredHeaders(t *testing.T) {
	var requests atomic.Int32
	preloaded := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/datasources" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer startup-token" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		if r.Host != "grafana.internal" {
			http.Error(w, "unexpected host", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `[{"id":3,"uid":"startup-prom","orgId":1,"type":"prometheus","access":"proxy"}]`)
		close(preloaded)
	}))
	defer origin.Close()

	client, cache := newGrafanaTestClient(t, origin.URL)
	defer cache.Close()
	client.Configuration().Paths[0].RequestHeaders = map[string]string{
		"Authorization": "Bearer startup-token",
		"Host":          "grafana.internal",
	}
	client.Handlers()
	select {
	case <-preloaded:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Grafana data source preload")
	}

	headers := client.discoveryHeaders(nil)
	ref := dataSourceRef{kind: dataSourceRefUID, value: "startup-prom"}
	key := dataSourceKey(discoveryIdentity(headers), ref.kind, ref.value)
	deadline := time.Now().Add(2 * time.Second)
	for client.loadDataSource(key) == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if client.loadDataSource(key) == nil {
		t.Fatal("preloaded data source was not published")
	}
	if _, err := client.resolveDataSource(context.Background(), ref, headers); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("preloaded lookup made an additional API request; got %d", got)
	}
}
