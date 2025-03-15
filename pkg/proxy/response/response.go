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

package response

import (
	"fmt"
	"io"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

var contentTypeHints = map[byte]string{
	0: fmt.Sprintf("%s; charset=UTF-8", headers.ValueApplicationJSON),
	1: fmt.Sprintf("%s; charset=UTF-8", headers.ValueApplicationJSON),
	2: fmt.Sprintf("%s; charset=UTF-8", headers.ValueApplicationCSV),
}

func WriteResponseHeader(w io.Writer, statusCode int, contentTypeHint byte,
	h http.Header) error {
	if rw, ok := w.(http.ResponseWriter); ok {
		rwh := rw.Header()
		if cth, ok := contentTypeHints[contentTypeHint]; ok && cth != "" {
			rwh.Set(headers.NameContentType, cth)
		}
		for k, v := range h {
			if len(v) == 0 {
				continue
			}
			rwh.Set(k, v[0])
		}
		if statusCode == 0 {
			statusCode = http.StatusOK
		}
		rw.WriteHeader(statusCode)
	}
	return nil
}
