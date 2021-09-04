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

package rewriter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
)

func TestProcessConfig(t *testing.T) {

	_, err := ProcessConfigs(nil)
	if err != errInvalidRewriterOptions {
		return
	}

	o := &options.Options{Instructions: testRL0}
	ri, err := ProcessConfigs(map[string]*options.Options{"test": o})
	if err != errInvalidRewriterOptions {
		t.Error("expected error for invalid rewriter options", err)
	}

	o2 := &options.Options{Instructions: testRLW1}

	ri, err = ProcessConfigs(map[string]*options.Options{"test": o, "rewriter1": o2})
	if err != nil {
		t.Error(err)
	}

	for _, v := range ri {
		h := Rewrite(v, &http.ServeMux{})
		r, _ := http.NewRequest("GET", "http://example.com/", nil)
		r.Header = make(http.Header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		// we only care about the first one
		break
	}

	o = &options.Options{Instructions: options.RewriteList{[]string{"method", "invalid", "POST"}}}
	_, err = ProcessConfigs(map[string]*options.Options{"test": o})
	if err == nil {
		t.Error("expected error for invalid instruction")
	}

}
