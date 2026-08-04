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

	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestHandleFirstResponseNilPool(t *testing.T) {
	h := &handler{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
	}
}

func TestHandleFirstResponse(t *testing.T) {
	p, _, _ := albpool.New(0, nil)
	defer p.Stop()
	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	p2, _, _ := albpool.NewHealthy(
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
	defer p2.Stop()
	h.SetPool(p2)
	albpool.WaitHealthy(t, p2, 1)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	p3, _, _ := albpool.NewHealthy(
		[]http.Handler{
			http.HandlerFunc(tu.BasicHTTPHandler),
			http.HandlerFunc(tu.BasicHTTPHandler),
		})
	defer p3.Stop()
	h.SetPool(p3)
	albpool.WaitHealthy(t, p3, 2)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))
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
	t.Run("FGR default rejects 4xx", func(t *testing.T) {
		p, _, _ := albpool.NewHealthy([]http.Handler{
			albpool.StatusHandler(http.StatusInternalServerError, "bad"),
			albpool.StatusHandler(http.StatusOK, "good"),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{fgr: true}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 got %d", w.Code)
		}
	})

	t.Run("FGR custom codes", func(t *testing.T) {
		codes := sets.New([]int{http.StatusAccepted})
		p, _, _ := albpool.NewHealthy([]http.Handler{
			albpool.StatusHandler(http.StatusOK, "not-accepted"),
			albpool.StatusHandler(http.StatusAccepted, "accepted"),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{fgr: true, fgrCodes: codes}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusAccepted {
			t.Errorf("expected 202 got %d", w.Code)
		}
	})

	t.Run("FGR fallback when none qualify", func(t *testing.T) {
		p, _, _ := albpool.NewHealthy([]http.Handler{
			albpool.StatusHandler(http.StatusInternalServerError, "err1"),
			albpool.StatusHandler(http.StatusBadGateway, "err2"),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{fgr: true}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusInternalServerError && w.Code != http.StatusBadGateway {
			t.Errorf("expected 500 or 502, got %d", w.Code)
		}
	})

	t.Run("FGR 50-backend partial fail", func(t *testing.T) {
		hs := make([]http.Handler, 50)
		for i := range hs {
			hs[i] = albpool.StatusHandler(http.StatusInternalServerError, "bad")
		}
		hs[37] = albpool.StatusHandler(http.StatusOK, "good")
		p, _, _ := albpool.NewHealthy(hs)
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(hs))

		h := &handler{fgr: true}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusOK {
			t.Errorf("expected 200 got %d", w.Code)
		}
	})

	t.Run("FGR 50-backend all fail fallback", func(t *testing.T) {
		hs := make([]http.Handler, 50)
		for i := range hs {
			hs[i] = albpool.StatusHandler(http.StatusInternalServerError, "err")
		}
		p, _, _ := albpool.NewHealthy(hs)
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(hs))

		h := &handler{fgr: true}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code < 400 {
			t.Errorf("expected 4xx/5xx fallback, got %d", w.Code)
		}
	})
}

func TestFGRFallbackEmits502WhenNoMemberQualifies(t *testing.T) {
	codes := sets.New([]int{http.StatusOK})
	p, _, _ := albpool.NewHealthy([]http.Handler{
		albpool.StatusHandler(http.StatusInternalServerError, "body0"),
		albpool.StatusHandler(http.StatusInternalServerError, "body1"),
	})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{fgr: true, fgrCodes: codes}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 got %d (body %q)", w.Code, w.Body.String())
	}
	if body := w.Body.String(); body == "body0" || body == "body1" {
		t.Errorf("fallback served disqualified upstream body %q", body)
	}
}

// TestHandleFirstResponseContextCancel verifies that cancelling the request
// context while a backend is responding does not race on the ResponseWriter.
// The raceWriter makes the race detector catch any write-after-return.
//
// Handlers observe the request context so cancelled iterations drain
// promptly: WaitForFirst returns on winner-claim without draining losers, so
// uncooperative handlers would orphan scatter goroutines past goleak's window.
func TestHandleFirstResponseContextCancel(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	for i := range 100 {
		func() {
			p, _, _ := albpool.New(-1, []http.Handler{slow, slow})
			defer p.Stop()
			p.SetHealthy([]http.Handler{slow, slow})

			h := &handler{}
			h.SetPool(p)
			ctx, cancel := context.WithCancel(context.Background())
			r := albpool.NewParentGET(t).WithContext(ctx)
			rw := &raceWriter{ResponseWriter: httptest.NewRecorder()}

			done := make(chan struct{})
			go func() {
				h.ServeHTTP(rw, r)
				close(done)
			}()

			time.Sleep(5 * time.Millisecond)
			cancel()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Errorf("iteration %d: ServeHTTP did not return after context cancel", i)
			}

			rw.returned = true
		}()
	}
}

func TestHandleFirstResponseContextCancel_50Backends(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	hs := make([]http.Handler, 50)
	for i := range hs {
		hs[i] = slow
	}
	for range 10 {
		func() {
			p, _, _ := albpool.New(-1, hs)
			defer p.Stop()
			p.SetHealthy(hs)

			h := &handler{}
			h.SetPool(p)
			ctx, cancel := context.WithCancel(context.Background())
			r := albpool.NewParentGET(t).WithContext(ctx)
			rw := &raceWriter{ResponseWriter: httptest.NewRecorder()}

			done := make(chan struct{})
			go func() {
				h.ServeHTTP(rw, r)
				close(done)
			}()
			time.Sleep(5 * time.Millisecond)
			cancel()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("ServeHTTP did not return after context cancel")
			}
			rw.returned = true
		}()
	}
}
