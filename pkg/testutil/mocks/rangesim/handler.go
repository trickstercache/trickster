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

package rangesim

import (
	"io"
	"net/http"
	"strconv"
	"time"
)

// Path is the route prefix that Register mounts the handler on.
const Path = "/byterange/"

// Register mounts the range simulator at Path. The max-age, status, ims, non-ims
// and size query parameters customize its responses.
func Register(mux *http.ServeMux) {
	mux.HandleFunc(Path, handler)
}

var customStatuses = map[string]int{
	"200": http.StatusOK,
	"206": http.StatusPartialContent,
	"304": http.StatusNotModified,
	"404": http.StatusNotFound,
	"500": http.StatusInternalServerError,
	"400": http.StatusBadRequest,
	"412": http.StatusRequestedRangeNotSatisfiable, // tests may depend on 412 answering 416
}

func handler(w http.ResponseWriter, r *http.Request) {
	rh := r.Header
	h := w.Header()
	qp := r.URL.Query()

	// a max-age that is invalid or <= 0 omits Cache-Control
	rMaxAge := maxAge
	if v := qp.Get("max-age"); v != "" {
		rMaxAge = ""
		if i, err := strconv.ParseInt(v, 10, 64); err == nil && i > 0 {
			rMaxAge = "max-age=" + strconv.FormatInt(i, 10)
		}
	}

	var customCode int
	if code, ok := customStatuses[qp.Get("status")]; ok {
		customCode = code
		// a forced 200 always returns the full body
		if code == http.StatusOK {
			rh.Del(hnIfModifiedSince)
			rh.Del(hnRange)
		}
	}

	// ims and non-ims force a code only when If-Modified-Since is or is not sent
	if customCode == 0 {
		if ims := rh.Get(hnIfModifiedSince); ims != "" {
			if code, ok := customStatuses[qp.Get("ims")]; ok {
				customCode = code
				if code == http.StatusOK {
					rh.Del(hnRange)
				}
			} else if t, err := time.Parse(time.RFC1123, ims); err == nil && !LastModified.After(t) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		} else if code, ok := customStatuses[qp.Get("non-ims")]; ok {
			customCode = code
			if code == http.StatusOK {
				rh.Del(hnRange)
			}
		}
	}

	if customCode > 299 {
		w.WriteHeader(customCode)
		return
	}

	if rMaxAge != "" {
		h.Add(hnCacheControl, rMaxAge)
	}
	h.Add(hnLastModified, LastModified.UTC().Format(time.RFC1123))

	if customCode == http.StatusOK {
		w.WriteHeader(customCode)
	}

	cl := contentLength
	if v := qp.Get("size"); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil && i > 0 {
			if i > MaxSize {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cl = i
		}
	}

	if ranges := parseRangeHeader(rh.Get(hnRange)); len(ranges) > 0 {
		lr := len(ranges)
		if ranges[lr-1].end > cl {
			cl = ranges[lr-1].end
		}
		if !ranges.validate(cl) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		// overlapping ranges could otherwise multiply MaxSize; like an excessive
		// range count, an oversized range set is ignored in favor of the full body
		if ranges.totalLength() <= MaxSize {
			if lr == 1 {
				h.Add(hnContentRange, ranges[0].contentRangeHeader(cl))
				h.Set(hnContentType, contentType)
				w.WriteHeader(http.StatusPartialContent)
				_ = writeRange(w, ranges[0])
				return
			}
			h.Set(hnContentType, hvMultipartByteRange+separator)
			w.WriteHeader(http.StatusPartialContent)
			_ = ranges.writeMultipartResponse(cl, w)
			return
		}
	}

	h.Set(hnContentLength, strconv.FormatInt(cl, 10))
	h.Set(hnContentType, contentType)
	h.Set(hnAcceptRanges, "bytes")
	// stops at the first failed write, so a departed client ends the response
	for j := int64(0); j < cl; j += contentLength {
		if _, err := io.WriteString(w, Body[:min(contentLength, cl-j)]); err != nil {
			return
		}
	}
}
