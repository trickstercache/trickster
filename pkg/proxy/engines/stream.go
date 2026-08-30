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
	"io"
	"mime"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// ContentTypeEventStream is the MIME type for Server-Sent Events
const ContentTypeEventStream = "text/event-stream"

func isStreamingResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	if ct := resp.Header.Get(headers.NameContentType); ct != "" {
		if base, _, err := mime.ParseMediaType(ct); err == nil &&
			base == ContentTypeEventStream {
			return true
		}
	}
	// an unknown length means the origin is streaming rather than sending a
	// complete object, so bytes must not wait for net/http's buffer to fill
	return resp.ContentLength == -1
}

type flushWriter struct {
	w     io.Writer
	flush func() error
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if n > 0 {
		// a failed flush does not invalidate bytes already written
		_ = f.flush()
	}
	return n, err
}

func streamWriter(w io.Writer, resp *http.Response) io.Writer {
	if w == nil || !isStreamingResponse(resp) {
		return w
	}
	rw, ok := w.(http.ResponseWriter)
	if !ok {
		return w
	}
	return &flushWriter{w: w, flush: http.NewResponseController(rw).Flush}
}

// A copy that ends early otherwise terminates the response normally, so the
// client sees a truncated body as a complete one; panicking breaks the
// connection instead. Only applies when serving a client under an http.Server.
func abortOnCopyError(w io.Writer, r *http.Request, err error) {
	if err == nil || r == nil {
		return
	}
	if _, ok := w.(http.ResponseWriter); !ok {
		return
	}
	if r.Context().Value(http.ServerContextKey) == nil {
		return
	}
	panic(http.ErrAbortHandler)
}
