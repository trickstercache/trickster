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

package consul

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeConsul implements enough of /v1/health/service/:service to exercise
// the blocking-query contract for real: a monotonic index, requests that
// park until the index moves past the one they carry, and the ability to
// force the pathological cases (index going backwards, a missing header).
//
// Testing against a stub that answers immediately would leave the parts of
// this provider most likely to be wrong -- the cursor handling and the
// blocking loop -- entirely uncovered.
type fakeConsul struct {
	*httptest.Server

	mtx      sync.Mutex
	entries  []serviceEntry
	index    uint64
	waiters  []chan struct{}
	status   int
	omitIdx  bool
	forceIdx uint64

	reqs     chan recordedRequest
	blocking sync.WaitGroup
}

type recordedRequest struct {
	Query  map[string][]string
	Header http.Header
	Path   string
}

func newFakeConsul(t *testing.T, entries ...serviceEntry) *fakeConsul {
	t.Helper()
	f := &fakeConsul{
		entries: entries,
		index:   100,
		status:  http.StatusOK,
		reqs:    make(chan recordedRequest, 256),
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.releaseAll()
		f.Close()
	})
	return f
}

func (f *fakeConsul) serve(w http.ResponseWriter, r *http.Request) {
	select {
	case f.reqs <- recordedRequest{
		Query: r.URL.Query(), Header: r.Header.Clone(), Path: r.URL.Path,
	}:
	default:
	}

	f.mtx.Lock()
	status := f.status
	f.mtx.Unlock()
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	// honor the blocking-query contract: park until the index moves past
	// the cursor the client sent, OR until the client's wait elapses.
	//
	// Honoring wait is not a detail. It is how a client recovers when the
	// server's index has gone backwards: the client cannot learn that while
	// parked, so the timeout is what hands it a response carrying the lower
	// index. A fake that parks indefinitely would make that recovery look
	// broken, and would leave the timeout path -- the common case for a
	// stable service -- untested.
	if idxStr := r.URL.Query().Get("index"); idxStr != "" {
		clientIdx, _ := strconv.ParseUint(idxStr, 10, 64)
		f.mtx.Lock()
		if f.index <= clientIdx {
			ch := make(chan struct{})
			f.waiters = append(f.waiters, ch)
			f.mtx.Unlock()
			f.blocking.Add(1)
			timer := time.NewTimer(parseWait(r.URL.Query().Get("wait")))
			select {
			case <-ch:
			case <-timer.C:
			case <-r.Context().Done():
			}
			timer.Stop()
			f.blocking.Done()
		} else {
			f.mtx.Unlock()
		}
	}

	f.mtx.Lock()
	entries, index, omit, force := f.entries, f.index, f.omitIdx, f.forceIdx
	f.mtx.Unlock()
	if force > 0 {
		index = force
	}
	if !omit {
		w.Header().Set(indexHeader, strconv.FormatUint(index, 10))
	}
	body, _ := json.Marshal(entries)
	w.Write(body)
}

// update replaces the catalog and advances the index, waking parked
// requests -- the exact sequence a real Consul performs on a change.
func (f *fakeConsul) update(entries ...serviceEntry) {
	f.mtx.Lock()
	f.entries = entries
	f.index += 10
	f.mtx.Unlock()
	f.releaseAll()
}

// bumpIndexOnly advances the index without changing the catalog, modelling
// a change to some other aspect of the service.
func (f *fakeConsul) bumpIndexOnly() {
	f.mtx.Lock()
	f.index += 10
	f.mtx.Unlock()
	f.releaseAll()
}

// rewindIndex models a Consul whose state was reset: a restarted server, or
// a service deleted and recreated. Clients must notice and start over
// rather than block forever on a cursor the server will never reach.
func (f *fakeConsul) rewindIndex(to uint64) {
	f.mtx.Lock()
	f.index = to
	f.mtx.Unlock()
	f.releaseAll()
}

func (f *fakeConsul) setStatus(code int) {
	f.mtx.Lock()
	f.status = code
	f.mtx.Unlock()
	f.releaseAll()
}

func (f *fakeConsul) setOmitIndexHeader(omit bool) {
	f.mtx.Lock()
	f.omitIdx = omit
	f.mtx.Unlock()
	f.releaseAll()
}

func (f *fakeConsul) releaseAll() {
	f.mtx.Lock()
	waiters := f.waiters
	f.waiters = nil
	f.mtx.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// nextRequest returns the next request the server saw.
func (f *fakeConsul) nextRequest(t *testing.T) recordedRequest {
	t.Helper()
	select {
	case r := <-f.reqs:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a request to consul")
		return recordedRequest{}
	}
}

// parseWait interprets the wait parameter in the form Consul accepts.
// Consul clamps anything over 10 minutes and defaults to 5 minutes when the
// parameter is absent or unusable.
func parseWait(v string) time.Duration {
	const fallback = 5 * time.Minute
	if v == "" {
		return fallback
	}
	unit := v[len(v)-1]
	n, err := strconv.Atoi(v[:len(v)-1])
	if err != nil || n <= 0 {
		return fallback
	}
	switch unit {
	case 's':
		return time.Duration(n) * time.Second
	case 'm':
		return time.Duration(n) * time.Minute
	default:
		return fallback
	}
}

// entry builds a catalog entry for tests.
func entry(id, addr string, port int, status string) serviceEntry {
	return serviceEntry{
		Node: node{Node: "node-1", Address: "10.9.9.9", Datacenter: "dc1"},
		Service: service{
			ID: id, Service: "web", Address: addr, Port: port,
		},
		Checks: []check{{CheckID: "c1", Status: status}},
	}
}
