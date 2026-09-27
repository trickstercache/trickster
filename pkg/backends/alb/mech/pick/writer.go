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
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	"github.com/trickstercache/trickster/v2/pkg/lb"
)

// firstWriteWriter reports the first byte written to the client, which is the latency signal
// of an HTTP flow, and keeps the status code so the flow's outcome can be judged. For an ALB
// that keeps sessions, the first write is also when the session's token is issued.
type firstWriteWriter struct {
	http.ResponseWriter
	pick     lb.Pick
	persist  *sticky.HTTP
	session  *sticky.Session
	req      *http.Request
	code     int
	wrote    bool
	finished bool
	// local holds the session of a request that no nested ALB will see
	local sticky.Session
}

var writerPool = sync.Pool{New: func() any { return &firstWriteWriter{} }}

func getWriter(w http.ResponseWriter, pk lb.Pick) *firstWriteWriter {
	fw := writerPool.Get().(*firstWriteWriter)
	fw.ResponseWriter, fw.pick, fw.code, fw.wrote, fw.finished = w, pk, http.StatusOK, false, false
	return fw
}

func putWriter(fw *firstWriteWriter) {
	fw.ResponseWriter, fw.pick = nil, lb.Pick{}
	fw.persist, fw.session, fw.req = nil, nil, nil
	writerPool.Put(fw)
}

func (w *firstWriteWriter) first(code int) {
	if w.wrote {
		return
	}
	w.wrote, w.code = true, code
	w.pick.FirstByte()
	w.finish()
}

// finish issues the session's token or stores its pins, once, while the headers can still change
func (w *firstWriteWriter) finish() {
	if w.persist == nil || w.finished {
		return
	}
	w.finished = true
	w.persist.Finish(w.ResponseWriter.Header(), w.req, w.session)
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

// Flush sends the headers, when not yet sent, and whatever is buffered; see FlushError.
func (w *firstWriteWriter) Flush() {
	_ = w.FlushError()
}

// FlushError flushes through every writer beneath, including those that only Unwrap, such as the
// access log's; a flush before any write sends the headers, so it is the first write.
func (w *firstWriteWriter) FlushError() error {
	w.first(http.StatusOK)
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack hands the connection to a handler that writes its own response from the header map, as a
// protocol switch does: the session is finished first, and a value to learn is read as it is written.
func (w *firstWriteWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil || w.wrote {
		return conn, brw, err
	}
	w.wrote, w.code = true, http.StatusSwitchingProtocols
	w.pick.FirstByte()
	if w.persist == nil {
		return conn, brw, nil
	}
	w.finished = true
	learn := w.persist.FinishSwitch(w.ResponseWriter.Header(), w.req, w.session)
	if learn == nil {
		return conn, brw, nil
	}
	lw := &learnOnWrite{w: brw.Writer, learn: learn}
	return conn, bufio.NewReadWriter(brw.Reader, bufio.NewWriterSize(lw, brw.Writer.Size())), nil
}

// learnOnWrite learns from the response headers at the first write through it, when a hijacker's
// response is on its way, and flushes every write, as the hijacker may go on with the bare conn
type learnOnWrite struct {
	w     *bufio.Writer
	learn func()
}

func (l *learnOnWrite) Write(b []byte) (int, error) {
	if l.learn != nil {
		l.learn()
		l.learn = nil
	}
	n, err := l.w.Write(b)
	if err == nil {
		err = l.w.Flush()
	}
	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *firstWriteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
