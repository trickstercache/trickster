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

package request

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func SetBody(r *http.Request, body []byte) *http.Request {

	if len(body) == 0 {
		r.Body = io.NopCloser(bytes.NewReader([]byte{}))
		r.ContentLength = 0
		r.Header.Del(headers.NameContentLength)
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	r.Header.Set(headers.NameContentLength, strconv.Itoa(len(body)))
	r.ContentLength = int64(len(body))
	return r.WithContext(tctx.WithRequestBody(r.Context(), body))
}

func GetBody(r *http.Request) []byte {
	body := tctx.RequestBody(r.Context())
	if body != nil {
		return body
	}
	if r.Body == nil {
		return nil
	}
	body, _ = io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body)) // allows body to be re-read from byte 0
	return body
}
