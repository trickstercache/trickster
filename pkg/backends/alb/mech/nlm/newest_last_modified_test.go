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

package nlm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestHandleNewestResponseNilPool(t *testing.T) {
	h := &handler{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))
	if w.Code != http.StatusBadGateway {
		t.Errorf("expected %d got %d", http.StatusBadGateway, w.Code)
	}
}

func TestHandleNewestResponse(t *testing.T) {
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

func TestNewestLastModifiedSelection(t *testing.T) {
	older := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	handlerWithLM := func(body string, lm time.Time) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// http.TimeFormat is IMF-fixdate (RFC 7231) with the literal "GMT"
			// suffix that real HTTP servers emit. time.RFC1123 formatted with
			// a UTC time produces "UTC" which no RFC 7231 parser accepts.
			w.Header().Set(headers.NameLastModified, lm.UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(body))
		})
	}

	t.Run("picks newest", func(t *testing.T) {
		p, _, _ := albpool.NewHealthy([]http.Handler{
			handlerWithLM("older", older),
			handlerWithLM("newer", newer),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 got %d", w.Code)
		}
		if w.Body.String() != "newer" {
			t.Errorf("expected body 'newer' got %q", w.Body.String())
		}
	})

	t.Run("invalid Last-Modified ignored", func(t *testing.T) {
		// One backend returns a valid date, the other returns garbage.
		// The valid one should be selected.
		badLM := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set(headers.NameLastModified, "not-a-date")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("bad"))
		})
		p, _, _ := albpool.NewHealthy([]http.Handler{
			badLM,
			handlerWithLM("valid", newer),
		})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Body.String() != "valid" {
			t.Errorf("expected body 'valid' got %q", w.Body.String())
		}
	})

	t.Run("fallback when no Last-Modified", func(t *testing.T) {
		noLM := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("fallback"))
		})
		p, _, _ := albpool.NewHealthy([]http.Handler{noLM, noLM})
		defer p.Stop()
		albpool.WaitHealthy(t, p, 2)

		h := &handler{}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 got %d", w.Code)
		}
		if w.Body.String() != "fallback" {
			t.Errorf("expected body 'fallback' got %q", w.Body.String())
		}
	})

	t.Run("50 picks newest", func(t *testing.T) {
		base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
		hs := make([]http.Handler, 50)
		for i := range hs {
			lm := base.AddDate(0, i, 0)
			body := fmt.Sprintf("b%02d", i)
			hs[i] = handlerWithLM(body, lm)
		}
		p, _, _ := albpool.NewHealthy(hs)
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(hs))

		h := &handler{}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 got %d", w.Code)
		}
		if w.Body.String() != "b49" {
			t.Errorf("expected newest body 'b49' got %q", w.Body.String())
		}
	})

	t.Run("50 partial fail picks only valid", func(t *testing.T) {
		err500 := albpool.StatusHandler(http.StatusInternalServerError, "")
		hs := make([]http.Handler, 50)
		for i := range hs {
			hs[i] = err500
		}
		hs[23] = handlerWithLM("winner", newer)
		p, _, _ := albpool.NewHealthy(hs)
		defer p.Stop()
		albpool.WaitHealthy(t, p, len(hs))

		h := &handler{}
		h.SetPool(p)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, albpool.NewParentGET(t))
		if w.Body.String() != "winner" {
			t.Errorf("expected 'winner' got %q (code %d)", w.Body.String(), w.Code)
		}
	})
}

func TestHandleNewestContextCancel(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Header().Set(headers.NameLastModified, time.Now().UTC().Format(time.RFC1123))
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
			w := httptest.NewRecorder()

			done := make(chan struct{})
			go func() {
				h.ServeHTTP(w, r)
				close(done)
			}()
			time.Sleep(5 * time.Millisecond)
			cancel()

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("ServeHTTP did not return after context cancel")
			}
		}()
	}
}
