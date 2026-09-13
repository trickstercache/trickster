/*
 * Copyright 2026 The Trickster Authors
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

package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/stretchr/testify/require"
)

type mirrorTarget struct {
	backends.Backend
	handler http.Handler
}

func (m mirrorTarget) Router() http.Handler { return m.handler }
func (m mirrorTarget) Name() string         { return "shadow" }

func newMirrorTarget(t *testing.T, h http.HandlerFunc) backends.Backend {
	t.Helper()
	b, err := backends.New("shadow", bo.New(), nil, nil, nil)
	require.NoError(t, err)
	return mirrorTarget{Backend: b, handler: h}
}

func TestMirror(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	require.Nil(t, Mirror("b", nil, nil, nil))
	require.Equal(t, reflect.ValueOf(next).Pointer(),
		reflect.ValueOf(Mirror("b", &po.MirrorOptions{BackendName: "shadow"}, nil, next)).Pointer())

	var mu sync.Mutex
	var seen []*http.Request
	var bodies []string
	done := make(chan struct{}, 16)
	target := newMirrorTarget(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, r)
		bodies = append(bodies, string(b))
		mu.Unlock()
		w.Header().Set("X-Discarded", "yes")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("discarded"))
		done <- struct{}{}
	})
	h := Mirror("b", &po.MirrorOptions{BackendName: "shadow"}, target, next)

	// a bodied request is buffered so both the route and the mirror read it
	r := httptest.NewRequest(http.MethodPost, "http://example.com/api?x=1", strings.NewReader("payload"))
	r.Header.Set("X-Tenant", "gold")
	r = request.SetResources(r, &request.Resources{})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	require.Equal(t, http.StatusNoContent, w.Code, "the mirror never touches the client response")
	require.Empty(t, w.Header().Get("X-Discarded"))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mirrored request was not served")
	}
	mu.Lock()
	require.Len(t, seen, 1)
	require.Equal(t, "payload", bodies[0])
	require.Equal(t, "/api", seen[0].URL.Path)
	require.Equal(t, "x=1", seen[0].URL.RawQuery)
	require.Equal(t, "example.com", seen[0].Host)
	require.Equal(t, "gold", seen[0].Header.Get("X-Tenant"))
	require.True(t, tctx.IsMirrored(seen[0].Context()))
	require.Nil(t, request.GetResources(seen[0]), "the copy carries none of the original's resources")
	mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	require.Equal(t, "payload", string(body), "the route still reads the body")

	// a mirrored copy is never mirrored again
	r = httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	r = r.WithContext(tctx.WithMirrored(r.Context()))
	h.ServeHTTP(httptest.NewRecorder(), r)
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	require.Len(t, seen, 1)
	mu.Unlock()

	// a zero percent share never fires, and a body that cannot be read is dropped
	h = Mirror("b", &po.MirrorOptions{BackendName: "shadow", Percent: 1}, target, next)
	for range 20 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	}
	h = Mirror("b", &po.MirrorOptions{BackendName: "shadow"}, target, next)
	broken := httptest.NewRequest(http.MethodPost, "http://example.com/", errReader{})
	h.ServeHTTP(httptest.NewRecorder(), broken)
}

func TestMirrorInFlightBound(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	target := newMirrorTarget(t, func(w http.ResponseWriter, _ *http.Request) {
		started.Add(1)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Mirror("b", &po.MirrorOptions{BackendName: "shadow", MaxInFlight: 2}, target, next)
	for range 5 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	}
	require.Eventually(t, func() bool { return started.Load() == 2 }, time.Second, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
		return started.Load() >= 3
	}, time.Second, 5*time.Millisecond, "the bound is released as mirrored requests finish")
}

func TestMirrorRecoversPanic(t *testing.T) {
	done := make(chan struct{})
	target := newMirrorTarget(t, func(http.ResponseWriter, *http.Request) {
		defer close(done)
		panic("boom")
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := Mirror("b", &po.MirrorOptions{BackendName: "shadow"}, target, next)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mirrored request was not served")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
