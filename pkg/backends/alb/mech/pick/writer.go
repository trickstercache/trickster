/*
 * Copyright 2026 The Trickster Authors
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
package pick

import (
	"io"
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// firstWriteWriter reports the first byte written to the client, which is the latency signal
// of an HTTP flow, and keeps the status code so the flow's outcome can be judged
type firstWriteWriter struct {
	http.ResponseWriter
	pick  lb.Pick
	code  int
	wrote bool
}

var writerPool = sync.Pool{New: func() any { return &firstWriteWriter{} }}

func getWriter(w http.ResponseWriter, pk lb.Pick) *firstWriteWriter {
	fw := writerPool.Get().(*firstWriteWriter)
	fw.ResponseWriter, fw.pick, fw.code, fw.wrote = w, pk, http.StatusOK, false
	return fw
}

func putWriter(fw *firstWriteWriter) {
	fw.ResponseWriter, fw.pick = nil, lb.Pick{}
	writerPool.Put(fw)
}

func (w *firstWriteWriter) first(code int) {
	if w.wrote {
		return
	}
	w.wrote, w.code = true, code
	w.pick.FirstByte()
}

func (w *firstWriteWriter) WriteHeader(code int) {
	// an informational response is not yet the member's answer
	if code < 100 || code >= 200 || code == http.StatusSwitchingProtocols {
		w.first(code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *firstWriteWriter) Write(b []byte) (int, error) {
	w.first(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

// ReadFrom keeps the underlying writer's copy fast path, such as sendfile, reachable
func (w *firstWriteWriter) ReadFrom(r io.Reader) (int64, error) {
	w.first(http.StatusOK)
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(writerOnly{w.ResponseWriter}, r)
}

// writerOnly hides ReadFrom so io.Copy does not recurse into it
type writerOnly struct{ io.Writer }

func (w *firstWriteWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *firstWriteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
