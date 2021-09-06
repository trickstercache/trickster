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
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func testMergeFunc(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {

}

func TestHandleResponseMerge(t *testing.T) {

	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil, nil)
	rsc.ResponseMergeFunc = testMergeFunc
	rsc.IsMergeMember = true
	r = request.SetResources(r, rsc)

	p, _, _ := testPool(pool.TimeSeriesMerge, 0, nil)
	c := &Client{pool: p, mergePaths: []string{"/"}}
	w := httptest.NewRecorder()
	c.handleResponseMerge(w, r)
	if w.Code != http.StatusBadGateway {
		t.Error("expected 502 got", w.Code)
	}

	var st []*healthcheck.Status
	c.pool, _, st = testPool(pool.TimeSeriesMerge, -1,
		[]http.Handler{http.HandlerFunc(tu.BasicHTTPHandler)})
	st[0].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	c.handleResponseMerge(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	c.pool, _, st = testPool(pool.TimeSeriesMerge, -1,
		[]http.Handler{
			http.HandlerFunc(tu.BasicHTTPHandler),
			http.HandlerFunc(tu.BasicHTTPHandler),
		})
	st[0].Set(0)
	st[1].Set(0)
	time.Sleep(250 * time.Millisecond)

	w = httptest.NewRecorder()
	c.handleResponseMerge(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

	w = httptest.NewRecorder()
	c.mergePaths = nil
	c.handleResponseMerge(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}

}
