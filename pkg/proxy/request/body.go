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

	"github.com/trickstercache/trickster/v2/pkg/proxy/handlers/trickster/failures"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func SetBody(r *http.Request, body []byte) {
	if len(body) == 0 {
		r.Body = io.NopCloser(bytes.NewReader([]byte{}))
		r.ContentLength = 0
		r.Header.Del(headers.NameContentLength)
	} else {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.Header.Set(headers.NameContentLength, strconv.Itoa(len(body)))
		r.ContentLength = int64(len(body))
		if rsc := GetResources(r); rsc != nil {
			rsc.RequestBody = body // cache to avoid future calls to io.ReadAll
		}
	}
}

func GetBody(r *http.Request, maxSize ...int64) ([]byte, error) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut &&
		r.Method != http.MethodPatch {
		return nil, nil
	}
	rsc := GetResources(r)
	if rsc != nil && len(rsc.RequestBody) > 0 {
		return rsc.RequestBody, nil // returns the cached body if exists
	}
	if r.Body == nil {
		return nil, nil
	}
	var stopAt int64 = -1
	if len(maxSize) > 0 && maxSize[0] >= 0 {
		stopAt = maxSize[0] + 1
	}
	rdr := io.Reader(r.Body)
	if stopAt > 0 {
		rdr = io.LimitReader(rdr, stopAt)
	}
	body, err := io.ReadAll(rdr)
	if err != nil {
		return nil, err
	}
	r.Body.Close()

	var tooBigErr error
	if stopAt > 0 && int64(len(body)) > maxSize[0] {
		body = body[:len(body)-1]
		tooBigErr = failures.ErrPayloadTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(body)) // allows body to be re-read from byte 0
	if rsc != nil {
		rsc.RequestBody = body // cache to avoid future calls to io.ReadAll
	}
	return body, tooBigErr
}

func GetBodyReader(r *http.Request) (io.ReadCloser, error) {
	b, err := GetBody(r)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
