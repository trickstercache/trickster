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

package engines

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

const benchPOSTBodySize = 64 << 10

func BenchmarkNewProxyRequestPOST(b *testing.B) {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/",
		bytes.NewReader(bytes.Repeat([]byte{'a'}, benchPOSTBodySize)))
	r = request.SetResources(r, request.NewResources(&bo.Options{}, nil, nil, nil, nil, nil))
	if _, err := request.GetBody(r); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		pr := newProxyRequest(r, nil)
		_ = pr.Clone()
	}
}

func BenchmarkPartialBucketRequestPOST(b *testing.B) {
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/",
		bytes.NewReader(bytes.Repeat([]byte{'a'}, benchPOSTBodySize)))
	r = request.SetResources(r, request.NewResources(&bo.Options{}, nil, nil, nil, nil, nil))
	if _, err := request.GetBody(r); err != nil {
		b.Fatal(err)
	}
	rendered := []byte("query=rendered")
	b.ReportAllocs()
	for b.Loop() {
		rq, err := PartialBucketRequest(r.Context(), r)
		if err != nil {
			b.Fatal(err)
		}
		request.SetBody(rq, rendered)
	}
}
