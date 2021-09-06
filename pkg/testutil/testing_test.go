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

package testing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func TestNewTestServer(t *testing.T) {
	s := NewTestServer(200, "OK", map[string]string{"Expires": "-1"})
	if s == nil {
		t.Errorf("Expected server pointer, got %v", s)
	}

	resp, err := http.Get(s.URL)
	if err != nil {
		t.Error(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d", resp.StatusCode)
	}

}

func TestNewTestWebClient(t *testing.T) {
	s := NewTestWebClient()
	if s == nil {
		t.Errorf("Expected webclient pointer, got %v", s)
	}

	err := s.CheckRedirect(nil, nil)
	if err != http.ErrUseLastResponse {
		t.Error(err)
	}

}

func TestNewTestInstance(t *testing.T) {
	s, w, r, c, err := NewTestInstance("", nil, 200, "", nil, "test", "test", "debug")

	if s == nil {
		t.Errorf("Expected server pointer, got %v", "nil")
	}

	if r == nil {
		t.Errorf("Expected server pointer, got %v", "nil")
	}

	if c == nil {
		t.Errorf("Expected server pointer, got %v", "nil")
	}

	if w == nil {
		t.Errorf("Expected server pointer, got %v", "nil")
	}

	if err != nil {
		t.Error(err)
	}

	// cover promsim conditional and path generation

	f := func(*bo.Options) map[string]*po.Options {
		return map[string]*po.Options{
			"path1": {},
			"path2": {},
		}
	}

	s, _, _, _, err = NewTestInstance("", f, 200, "", nil, "promsim", "test", "debug")
	if s == nil {
		t.Error("Expected server pointer, got nil")
	}
	if err != nil {
		t.Error(err)
	}

	// cover config file provided

	_, _, _, _, err = NewTestInstance("../../../testdata/test.full.conf", f, 200, "", nil, "promsim", "test", "debug")
	if err == nil {
		t.Error("Expected error, got nil")
	}

	_, _, _, _, err = NewTestInstance("", nil, 200, "", map[string]string{"test-header": "x"}, "rangesim", "test", "debug")
	if err != nil {
		t.Error(err)
	}

}

func TestNewTestTracer(t *testing.T) {
	tr := NewTestTracer()
	if tr.Name != "test" {
		t.Error("expected test got", tr.Name)
	}
}

func TestBasicHTTPHandler(t *testing.T) {
	w := httptest.NewRecorder()
	BasicHTTPHandler(nil, nil) // cover nil writer case, success = no panic
	BasicHTTPHandler(w, nil)
	if w.Body.String() != "{}" {
		t.Error("basic handler error")
	}
}
