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

package tsm

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

var testLogger = logging.NoopLogger()

func testMergeFunc(w http.ResponseWriter, r *http.Request, rgs merge.ResponseGates) {

}

func TestHandleResponseMerge(t *testing.T) {
	logger.SetLogger(testLogger)
	r, _ := http.NewRequest("GET", "http://trickstercache.org/", nil)
	rsc := request.NewResources(nil, nil, nil, nil, nil, nil)
	rsc.ResponseMergeFunc = testMergeFunc
	rsc.IsMergeMember = true
	r = request.SetResources(r, rsc)

	p, _, _ := albpool.New(0, nil)
	h := &handler{pool: p, mergePaths: []string{"/"}}
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

	w = httptest.NewRecorder()
	h.mergePaths = nil
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Error("expected 200 got", w.Code)
	}
}
