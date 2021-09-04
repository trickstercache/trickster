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

package handlers

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestRedirector(t *testing.T) {
	ctx := context.Background()
	ctx = WithRedirects(ctx, 302, "http://trickstercache.org")
	r := httptest.NewRequest("GET", "http://0/trickster/", nil)
	w := httptest.NewRecorder()
	HandleRedirectResponse(w, r)
	if w.Result().StatusCode != 400 {
		t.Errorf("expected %d got %d", 400, w.Result().StatusCode)
	}

	r = r.WithContext(ctx)
	w = httptest.NewRecorder()
	HandleRedirectResponse(w, r)
	if w.Result().StatusCode != 302 {
		t.Errorf("expected %d got %d", 302, w.Result().StatusCode)
	}
}
