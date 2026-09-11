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
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

// errReader fails on first Read so request.CloneWithoutResources returns
// an error from io.ReadAll on the body.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errors.New("body read failure") }
func (errReader) Close() error               { return nil }

// When every fanout goroutine returns from CloneWithoutResources before
// captures[i] is populated, the fallback iterates an all-nil captures
// slice, never calls serve, and the main select blocks on responseWritten
// until r.Context() is canceled.
func TestFRDoesNotHangWhenAllTargetsAbort(t *testing.T) {
	never := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	p, _, _ := albpool.New(-1, []http.Handler{never, never})
	defer p.Stop()
	p.SetHealthy([]http.Handler{never, never})

	h := &handler{}
	h.SetPool(p)

	// POST with an erroring body causes CloneWithoutResources to fail in
	// every goroutine before captures[i] = crw runs.
	r, _ := http.NewRequest(http.MethodPost, "http://trickstercache.org/", errReader{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	r = r.WithContext(ctx)

	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, r)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP hung; expected return when all fanout goroutines abort")
	}

	if w.Code != http.StatusBadGateway {
		t.Errorf("got status %d, want %d", w.Code, http.StatusBadGateway)
	}
}
