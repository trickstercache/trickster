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

// Package blockingquery provides a test server implementing HashiCorp's
// blocking query protocol, shared by the consul and nomad provider tests.
//
// It exists because a stub that answers immediately would leave the parts of
// a blocking-query provider most likely to be wrong entirely uncovered: the
// cursor handling, and the loop's behavior when a request parks. In
// particular the server honors the client's wait parameter, which is not a
// detail -- the timeout is the common case for a stable service, and it is
// also the only way a client learns that the server's index has gone
// backwards, since it cannot learn that while parked.
package blockingquery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Request is one request the server saw.
type Request struct {
	Query  map[string][]string
	Header http.Header
	Path   string
}

// Server is a fake HashiCorp-style API endpoint.
type Server struct {
	*httptest.Server

	indexHeader string

	mtx      sync.Mutex
	body     []byte
	index    uint64
	waiters  []chan struct{}
	status   int
	omitIdx  bool
	forceIdx uint64

	reqs chan Request
}

// New returns a started Server that reports its cursor in the named header
// (X-Consul-Index, X-Nomad-Index), serving body as JSON. It is closed when
// the test ends.
func New(t *testing.T, indexHeader string, body any) *Server {
	t.Helper()
	s := &Server{
		indexHeader: indexHeader,
		index:       100,
		status:      http.StatusOK,
		reqs:        make(chan Request, 256),
	}
	s.SetBody(t, body)
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		s.releaseAll()
		s.Close()
	})
	return s
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	select {
	case s.reqs <- Request{
		Query: r.URL.Query(), Header: r.Header.Clone(), Path: r.URL.Path,
	}:
	default:
	}

	s.mtx.Lock()
	status := s.status
	s.mtx.Unlock()
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}

	// park until the index moves past the cursor the client sent, or until
	// the client's own wait elapses
	if idxStr := r.URL.Query().Get("index"); idxStr != "" {
		clientIdx, _ := strconv.ParseUint(idxStr, 10, 64)
		s.mtx.Lock()
		if s.index <= clientIdx {
			ch := make(chan struct{})
			s.waiters = append(s.waiters, ch)
			s.mtx.Unlock()
			timer := time.NewTimer(parseWait(r.URL.Query().Get("wait")))
			select {
			case <-ch:
			case <-timer.C:
			case <-r.Context().Done():
			}
			timer.Stop()
		} else {
			s.mtx.Unlock()
		}
	}

	s.mtx.Lock()
	body, index, omit, force := s.body, s.index, s.omitIdx, s.forceIdx
	s.mtx.Unlock()
	if force > 0 {
		index = force
	}
	if !omit {
		w.Header().Set(s.indexHeader, strconv.FormatUint(index, 10))
	}
	w.Write(body)
}

// SetBody replaces the served document without touching the index, for
// tests that need the two to move independently.
func (s *Server) SetBody(t *testing.T, body any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling fake response: %v", err)
	}
	s.mtx.Lock()
	s.body = b
	s.mtx.Unlock()
}

// Update replaces the document and advances the index, waking parked
// requests -- the sequence a real server performs on a change.
func (s *Server) Update(t *testing.T, body any) {
	t.Helper()
	s.SetBody(t, body)
	s.BumpIndex()
}

// BumpIndex advances the index without changing the document, modelling a
// change to some other aspect of the resource.
func (s *Server) BumpIndex() {
	s.mtx.Lock()
	s.index += 10
	s.mtx.Unlock()
	s.releaseAll()
}

// RewindIndex models a server whose state was reset: a restart, or a
// resource deleted and recreated. Clients must notice and start over rather
// than block forever on a cursor the server will never reach again.
func (s *Server) RewindIndex(to uint64) {
	s.mtx.Lock()
	s.index = to
	s.mtx.Unlock()
	s.releaseAll()
}

// SetStatus makes subsequent responses carry the given status code.
func (s *Server) SetStatus(code int) {
	s.mtx.Lock()
	s.status = code
	s.mtx.Unlock()
	s.releaseAll()
}

// SetOmitIndexHeader suppresses the cursor header, so clients must fall back
// to plain reads.
func (s *Server) SetOmitIndexHeader(omit bool) {
	s.mtx.Lock()
	s.omitIdx = omit
	s.mtx.Unlock()
	s.releaseAll()
}

func (s *Server) releaseAll() {
	s.mtx.Lock()
	waiters := s.waiters
	s.waiters = nil
	s.mtx.Unlock()
	for _, ch := range waiters {
		close(ch)
	}
}

// NextRequest returns the next request the server saw, failing the test if
// none arrives.
func (s *Server) NextRequest(t *testing.T) Request {
	t.Helper()
	select {
	case r := <-s.reqs:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a request")
		return Request{}
	}
}

// TryNextRequest returns the next recorded request if one is already
// waiting.
func (s *Server) TryNextRequest() (Request, bool) {
	select {
	case r := <-s.reqs:
		return r, true
	default:
		return Request{}, false
	}
}

// parseWait interprets the wait parameter in the form the HashiCorp APIs
// accept, defaulting to their five-minute default when absent or unusable.
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
