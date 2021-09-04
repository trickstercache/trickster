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

package prometheus

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

func TestProcessTransformations(t *testing.T) {
	// passing test case is no panics
	c := &Client{injectLabels: map[string]string{"test": "trickster"}}
	c.ProcessTransformations(nil)
	c.hasTransformations = true
	c.ProcessTransformations(nil)
	c.ProcessTransformations(&dataset.DataSet{})
}

func TestDefaultWrite(t *testing.T) {
	w := httptest.NewRecorder()
	defaultWrite(200, w, []byte("trickster"))
	if w.Body.String() != "trickster" || w.Code != 200 {
		t.Error("write mismatch")
	}
}

func TestProcessVectorTransformations(t *testing.T) {

	c := &Client{}
	w := httptest.NewRecorder()
	r, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	resp := &http.Response{StatusCode: 200}

	rsc := &request.Resources{}
	rg := merge.NewResponseGate(w, r, rsc)
	rg.Response = resp
	rg.Write([]byte("trickster"))
	c.processVectorTransformations(w, rg)
	if w.Code != 200 {
		t.Errorf("expected %d got %d", 200, w.Code)
	}

}
