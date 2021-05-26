/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package byterange

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

func writeError(code int, body []byte, w http.ResponseWriter) {
	w.WriteHeader(code)
	w.Write(body)
}

// InsertRoutes inserts the mock's routes into the provided Mux
func InsertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/byterange/", handler)
}

var customStatuses = map[string]int{
	"200": http.StatusOK,
	"206": http.StatusPartialContent,
	"304": http.StatusNotModified,
	"404": http.StatusNotFound,
	"500": http.StatusInternalServerError,
	"400": http.StatusBadRequest,
	"412": http.StatusRequestedRangeNotSatisfiable,
}

func handler(w http.ResponseWriter, r *http.Request) {

	rh := r.Header
	h := w.Header()

	// handle custom response code requested by the client for testing purposees

	customCode := 0

	var code int
	var ok bool

	// user can send max-age=XX to define a custom max-age header
	rMaxAge := maxAge
	if v := r.URL.Query().Get("max-age"); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			if i > 0 {
				rMaxAge = fmt.Sprintf("max-age=%d", i)
			} else {
				rMaxAge = ""
			}
		} else {
			rMaxAge = ""
		}
	}

	if code, ok = customStatuses[r.URL.Query().Get("status")]; ok {
		customCode = code
		// if the user custom-requested 200, go ahead and return the full body
		// to do that, we'll delete any IMS and Range headers from the client
		if code == http.StatusOK {
			rh.Del(hnIfModifiedSince)
			rh.Del(hnRange)
		}
	}

	if customCode == 0 {
		// if the client is revalidating and their copy is still fresh
		// reply w/ a 304 Not Modified
		if ims := rh.Get(hnIfModifiedSince); ims != "" {
			// for testing a 200 OK only when the user sends an IMS
			if code, ok := customStatuses[r.URL.Query().Get("ims")]; ok {
				customCode = code
				if code == http.StatusOK {
					rh.Del(hnRange)
				}
			} else {
				t, err := time.Parse(time.RFC1123, ims)
				if err == nil && (!lastModified.After(t)) {
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
		} else if code, ok := customStatuses[r.URL.Query().Get("non-ims")]; ok {
			// for testing a 200 OK only when the user does _not_ send IMS
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

	// add some cacheability headers
	if rMaxAge != "" {
		h.Add(hnCacheControl, rMaxAge)
	}

	h.Add(hnLastModified, lastModified.UTC().Format(time.RFC1123))

	if customCode == http.StatusOK {
		w.WriteHeader(customCode)
	}

	cl := contentLength
	if v := r.URL.Query().Get("size"); v != "" {
		if i, err := strconv.ParseInt(v, 10, 64); err == nil && i > 0 {
			cl = i
		}
	}

	// Handle Range Request Cases
	if cr := r.Header.Get(hnRange); cr != "" {
		ranges := parseRangeHeader(cr)
		lr := len(ranges)
		if ranges != nil && lr > 0 {
			if ranges[lr-1].end > cl {
				cl = ranges[lr-1].end
			}
			if ranges.validate(cl) {
				// Handle Single Range in Request
				if lr == 1 {
					h.Add(hnContentRange, ranges[0].contentRangeHeader(cl))
					h.Set(hnContentType, contentType)
					w.WriteHeader(http.StatusPartialContent)
					writeRange(w, ranges[0])
					return
				}
				// Handle Multiple Ranges in Request
				h.Set(hnContentType, hvMultipartByteRange+separator)
				w.WriteHeader(http.StatusPartialContent)
				ranges.writeMultipartResponse(cl, w)
				return
			}
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			// TODO: write correct response indictaing what was wrong with the range.
			return
		}
	}

	h.Set(hnContentLength, strconv.Itoa(int(cl)))
	h.Set(hnContentType, contentType)
	h.Set(hnAcceptRanges, "bytes")

	j := int64(0)
	lb := int64(len(Body))

	for int64(j) < cl {
		if cl-j < lb {
			w.Write([]byte(Body[:cl-j]))
		} else {
			w.Write([]byte(Body))
		}
		j += lb
	}

}

func writeRange(w io.Writer, br byteRange) {
	if br.end < contentLength {
		w.Write([]byte(Body[br.start : br.end+1]))
		return
	}
	cl := br.end - br.start
	bw := int64(0)
	offset := br.start % contentLength
	for bw < cl {
		left := cl - bw
		end := contentLength
		if left < contentLength-offset {
			end = offset + left
		}
		w.Write([]byte(Body[offset : end+1]))
		bw += (end - offset)
		offset = 0
	}
}
