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

func GetBody(r *http.Request) ([]byte, error) {
	rsc := GetResources(r)
	if rsc != nil && len(rsc.RequestBody) > 0 {
		return rsc.RequestBody, nil // returns the cached body if exists
	}
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body)) // allows body to be re-read from byte 0
	if rsc != nil {
		rsc.RequestBody = body // cache to avoid future calls to io.ReadAll
	}
	return body, nil
}
