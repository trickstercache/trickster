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

package fr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestHandleFirstResponseNilPool(t *testing.T) {
	h := &handler{}
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
	}
}

func TestHandleFirstResponse(t *testing.T) {
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)

	p, _, _ := albpool.New(0, nil)
	h := &handler{pool: p}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	var st []*healthcheck.Status
	h.pool, _, st = albpool.New(-1,
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
	st[0].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	h.pool, _, st = albpool.New(-1,
		[]http.Handler{
			http.HandlerFunc(tu.BasicHTTPHandler),
			http.HandlerFunc(tu.BasicHTTPHandler),
		})
	st[0].Set(0)
	st[1].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}
}

// raceWriter wraps an http.ResponseWriter with an intentionally unprotected
// bool. Write/WriteHeader/Header read the bool; the caller writes it after
// ServeHTTP returns. If any goroutine still touches the writer after return,
// the race detector catches the unsynchronized read+write.
type raceWriter struct {
	http.ResponseWriter
	returned bool // intentionally NOT synchronized
}

func (w *raceWriter) Header() http.Header {
	_ = w.returned // read
	return w.ResponseWriter.Header()
}

func (w *raceWriter) WriteHeader(code int) {
	_ = w.returned // read
	w.ResponseWriter.WriteHeader(code)
}

func (w *raceWriter) Write(b []byte) (int, error) {
	_ = w.returned // read
	return w.ResponseWriter.Write(b)
}

func TestFirstGoodResponse(t *testing.T) {
	statusHandler := func(code int, body string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(body))
		})
	}

	t.Run("FGR default rejects 4xx", func(t *testing.T) {
		// FGR mode without custom codes: any status < 400 qualifies.
		// Backend 0 returns 500, backend 1 returns 200.
		// FGR should pick the 200.
		p, _, st := albpool.New(-1, []http.Handler{
			statusHandler(http.StatusInternalServerError, "bad"),
			statusHandler(http.StatusOK, "good"),
		})
		st[0].Set(0)
		st[1].Set(0)
		time.Sleep(250 * time.Millisecond)

		h := &handler{pool: p, fgr: true}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
		h.ServeHTTP(w, r)
		// The "good" backend should be selected
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 got %d", w.Code)
		}
	})

	t.Run("FGR custom codes", func(t *testing.T) {
		// Only 202 is considered "good"
		codes := sets.New([]int{http.StatusAccepted})
		p, _, st := albpool.New(-1, []http.Handler{
			statusHandler(http.StatusOK, "not-accepted"),
			statusHandler(http.StatusAccepted, "accepted"),
		})
		st[0].Set(0)
		st[1].Set(0)
		time.Sleep(250 * time.Millisecond)

		h := &handler{pool: p, fgr: true, fgrCodes: codes}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
		h.ServeHTTP(w, r)
		if w.Code != http.StatusAccepted {
			t.Errorf("expected 202 got %d", w.Code)
		}
	})

	t.Run("FGR fallback when none qualify", func(t *testing.T) {
		// All backends return 500; none qualify. Fallback serves first available.
		p, _, st := albpool.New(-1, []http.Handler{
			statusHandler(http.StatusInternalServerError, "err1"),
			statusHandler(http.StatusBadGateway, "err2"),
		})
		st[0].Set(0)
		st[1].Set(0)
		time.Sleep(250 * time.Millisecond)

		h := &handler{pool: p, fgr: true}
		w := httptest.NewRecorder()
		r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
		h.ServeHTTP(w, r)
		// Fallback should serve one of the error responses
		if w.Code != http.StatusInternalServerError && w.Code != http.StatusBadGateway {
			t.Errorf("expected 500 or 502, got %d", w.Code)
		}
	})
}

// TestHandleFirstResponseContextCancel verifies that cancelling the request
// context while a backend is responding does not race on the ResponseWriter.
// The raceWriter makes the race detector catch any write-after-return.
func TestHandleFirstResponseContextCancel(t *testing.T) {
	// A handler that always writes a response (never short-circuits on
	// context cancel), maximizing the chance of a write landing after
	// ServeHTTP returns.
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	for i := range 100 {
		p, _, _ := albpool.New(-1, []http.Handler{slow, slow})
		p.SetHealthy([]http.Handler{slow, slow})

		h := &handler{pool: p}
		ctx, cancel := context.WithCancel(context.Background())
		r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
		r = r.WithContext(ctx)
		rw := &raceWriter{ResponseWriter: httptest.NewRecorder()}

		done := make(chan struct{})
		go func() {
			h.ServeHTTP(rw, r)
			close(done)
		}()

		// Cancel context while backends are in-flight to trigger the
		// r.Context().Done() select case in ServeHTTP.
		time.Sleep(5 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: ServeHTTP did not return after context cancel", i)
		}

		// Signal that the handler has returned. If a goroutine spawned by
		// ServeHTTP is still calling rw.Write/WriteHeader, the race
		// detector will flag the concurrent read of rw.returned above
		// against this write.
		rw.returned = true
	}
}
