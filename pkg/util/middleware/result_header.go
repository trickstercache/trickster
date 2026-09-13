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

package middleware

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// HideResultHeader returns a writer that withholds X-Trickster-Result from the client when the
// path that finally served the request hides it, moving its value onto the request's resources so
// the access log can still record the result; a dispatch to a path that exposes it is honored,
// since the resources carry the path the response came from
func HideResultHeader(w http.ResponseWriter, rsc *request.Resources) http.ResponseWriter {
	return &hiddenResultWriter{ResponseWriter: w, rsc: rsc}
}

type hiddenResultWriter struct {
	http.ResponseWriter
	rsc         *request.Resources
	wroteHeader bool
}

func (w *hiddenResultWriter) hides() bool {
	if w.rsc == nil {
		return true
	}
	return w.rsc.PathConfig == nil || w.rsc.PathConfig.HideResultHeader
}

func (w *hiddenResultWriter) WriteHeader(code int) {
	// the header is taken at the final status only; an informational response carries none,
	// and the map is read by the transport at this call, so it is taken before delegating
	if !w.wroteHeader && (code < 100 || code >= 200) {
		w.wroteHeader = true
		h := w.Header()
		if v := h.Get(headers.NameTricksterResult); v != "" && w.hides() {
			if w.rsc != nil {
				w.rsc.HiddenResult = v
			}
			h.Del(headers.NameTricksterResult)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *hiddenResultWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *hiddenResultWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
