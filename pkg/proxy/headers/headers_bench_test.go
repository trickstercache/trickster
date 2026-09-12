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

package headers

import (
	"net/http"
	"testing"
)

func BenchmarkUpdateHeaders(b *testing.B) {
	h := http.Header{"X-Del": {"x"}, "X-Set": {"old"}}
	updates := map[string]string{"X-Set": "new", "-X-Del": "", "+X-Add": "v"}
	b.ReportAllocs()
	for b.Loop() {
		UpdateHeaders(h, updates)
		delete(h, "X-Add")
	}
}

func BenchmarkUpdateRequestHeaders(b *testing.B) {
	r, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	updates := map[string]string{"Host": "tenant.example.com", "X-Set": "new", "-X-Del": ""}
	b.ReportAllocs()
	for b.Loop() {
		UpdateRequestHeaders(r, updates)
	}
}
