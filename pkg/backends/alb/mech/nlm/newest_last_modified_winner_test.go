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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestNLMFallbackPrefers2xxOver5xx(t *testing.T) {
	p, _, _ := albpool.NewHealthy([]http.Handler{
		albpool.StatusHandler(http.StatusInternalServerError, "body0"),
		albpool.StatusHandler(http.StatusOK, "body1"),
	})
	defer p.Stop()
	albpool.WaitHealthy(t, p, 2)

	h := &handler{}
	h.SetPool(p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, albpool.NewParentGET(t))

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 got %d", w.Code)
	}
	if w.Body.String() != "body1" {
		t.Errorf("expected body 'body1' got %q", w.Body.String())
	}
}
