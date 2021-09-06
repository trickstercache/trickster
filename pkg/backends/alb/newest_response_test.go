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

package alb

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func TestHandleNewestResponse(t *testing.T) {

	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)

	p, _, _ := testPool(pool.NewestLastModified, 0, nil)
	c := &Client{pool: p}
	w := httptest.NewRecorder()
	c.handleNewestResponse(w, r)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	var st []*healthcheck.Status
	c.pool, _, st = testPool(pool.NewestLastModified, -1,
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
	st[0].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	c.handleNewestResponse(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	c.pool, _, st = testPool(pool.NewestLastModified, -1,
		[]http.Handler{
			http.HandlerFunc(tu.BasicHTTPHandler),
			http.HandlerFunc(tu.BasicHTTPHandler),
		})
	st[0].Set(0)
	st[1].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	c.handleNewestResponse(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

}

func TestWriteHeader(t *testing.T) {
	w := httptest.NewRecorder()
	nrm := &newestResponseMux{}
	nrm.wg.Add(1)
	nrg := newNewestResponseGate(w, 0, nrm)
	nrg.WriteHeader(200)
}

func TestRegisterLM(t *testing.T) {
	nrm := &newestResponseMux{}
	b := nrm.registerLM(0, time.Time{})
	if b {
		t.Error("expected false")
	}
}
