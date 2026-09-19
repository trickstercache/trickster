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
package pgwire

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

const sqlstateQueryCanceled = "57014"

var (
	// errResultTooLarge means a rendered sub-query outgrew the buffering limits
	// and was canceled; the client's own statement is relayed instead.
	errResultTooLarge = errors.New("postgres result exceeds the buffering limits")
	// errRelayResumed means an oversized result of the client's own statement
	// was handed back to the relay mid-stream; the client is already being answered.
	errRelayResumed = errors.New("postgres result handed back to the relay")
)

type originError struct {
	body     []byte
	code     string
	rendered bool
}

func (e *originError) Error() string { return "postgres origin error " + e.code }

func newOriginError(body []byte) *originError {
	e := &originError{body: body}
	var response pgproto3.ErrorResponse
	if response.Decode(body) == nil {
		e.code = response.Code
	}
	return e
}

type rowReader struct {
	timeColumn int
	decoder    *timeAxisDecoder
	step       time.Duration
	phase      time.Duration
}

func (s *session) fetch(sql string, original bool, plan *deltaPlan) (*Result, error) {
	// buffers one statement's result from the borrowed origin connection. original marks
	// the client's own statement, whose result can go back to the relay if it outgrows the limits.
	config := &s.server.config
	if config.QueryTimeout > 0 {
		_ = s.upstream.SetReadDeadline(time.Now().Add(config.QueryTimeout))
	}
	if !writeAll(s.upstream, appendFrame(nil, msgQuery, append([]byte(sql), 0)), config.WriteTimeout) {
		return nil, io.ErrClosedPipe
	}
	result := &Result{}
	var (
		reader   *rowReader
		failure  error
		overflow bool
		header   [frameHeaderLen]byte
	)
	for {
		if _, err := io.ReadFull(s.upstreamReader, header[:]); err != nil {
			return nil, err
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length < frameLenSize || length-frameLenSize > pgMaxMessageBody {
			return nil, errFrameLength
		}
		size := int(length - frameLenSize)
		if header[0] == msgDataRow && !overflow && failure == nil {
			if result.Rows() >= config.MaxResultRows || len(result.data)+size > config.MaxResultSizeBytes {
				if original {
					return nil, s.resumeRelay(result, size)
				}
				overflow = true
				s.cancelFetch()
			}
		}
		body, err := s.readFetchBody(result, header[0], size, overflow || failure != nil)
		if err != nil {
			return nil, err
		}
		switch header[0] {
		case msgRowDescription:
			result.RowDescription = body
			if plan != nil {
				if reader, err = plan.rowReader(s, body); err != nil && failure == nil {
					failure = err
				}
			}
		case msgDataRow:
			if overflow || failure != nil {
				continue
			}
			if err = result.keepRow(size, reader); err != nil {
				failure = err
			}
		case msgCommandComplete:
			result.Tag = string(trimNUL(body))
		case msgErrorResponse:
			if failure == nil || overflow {
				failure = newOriginError(body)
			}
		case msgParameterStatus:
			s.observeParameterStatus(body)
			fallthrough
		case msgNotice, msgNotification:
			// asynchronous messages belong to the client whenever they arrive
			if !writeAll(s.client, appendFrame(nil, header[0], body), config.WriteTimeout) {
				return nil, io.ErrClosedPipe
			}
		case msgReadyForQuery:
			if len(body) > 0 {
				s.txStatus.Store(uint32(body[0]))
			}
			if overflow {
				return nil, errResultTooLarge
			}
			if failure != nil {
				return nil, failure
			}
			if reader != nil {
				result.sortByTime()
			}
			return result, nil
		}
	}
}

func (s *session) readFetchBody(result *Result, typ byte, size int, discard bool) ([]byte, error) {
	// reads a message body. A kept DataRow lands directly at the
	// end of the result's row data, so a row is copied exactly once.
	if typ == msgDataRow {
		if discard {
			_, err := io.CopyN(io.Discard, s.upstreamReader, int64(size))
			return nil, err
		}
		start := len(result.data)
		result.data = append(result.data, make([]byte, size)...)
		if _, err := io.ReadFull(s.upstreamReader, result.data[start:]); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if size > s.server.config.MaxResultSizeBytes {
		// no row description, notice or error is legitimately larger than a whole result may be
		return nil, errFrameLength
	}
	body := make([]byte, size)
	_, err := io.ReadFull(s.upstreamReader, body)
	return body, err
}

func (r *Result) keepRow(size int, reader *rowReader) error {
	// records the row just read into the result's data, with its bucket time.
	if reader == nil {
		r.ends = append(r.ends, uint32(len(r.data))) // #nosec G115 -- bounded by MaxResultSizeBytes
		return nil
	}
	start := len(r.data) - size
	bucket, err := bucketTime(r.data[start:], reader.timeColumn, reader.decoder, reader.step, reader.phase)
	if err != nil {
		r.data = r.data[:start]
		return err
	}
	r.ends = append(r.ends, uint32(len(r.data))) // #nosec G115 -- bounded by MaxResultSizeBytes
	r.times = append(r.times, bucket)
	return nil
}

func (s *session) resumeRelay(buffered *Result, pendingRowSize int) error {
	// Sends what was buffered and lets the origin pump forward the rest, so an oversized
	// result is neither canceled nor fetched twice. Nothing here is sized by the origin's row.
	buffer := pumpBuffers.Get().(*[]byte)
	defer pumpBuffers.Put(buffer)
	out := &frameWriter{conn: s.client, timeout: s.server.config.WriteTimeout, buffer: (*buffer)[:0]}
	if buffered.RowDescription != nil {
		out.frame(msgRowDescription, buffered.RowDescription)
	}
	for row := range buffered.ends {
		out.frame(msgDataRow, buffered.row(row))
	}
	// the row whose header was read is passed through whole, so the pump resumes at a boundary
	out.stream(msgDataRow, s.upstreamReader, pendingRowSize)
	if out.err != nil {
		return out.err
	}
	// the parked pump completes this request when it sees ReadyForQuery
	s.rows += int64(buffered.Rows() + 1)
	s.relayResumed = true
	s.countRequest(msgQuery)
	return errRelayResumed
}

type frameWriter struct {
	conn    net.Conn
	timeout time.Duration
	buffer  []byte
	err     error
}

func (w *frameWriter) flush() {
	if w.err == nil && len(w.buffer) > 0 && !writeAll(w.conn, w.buffer, w.timeout) {
		w.err = io.ErrClosedPipe
	}
	w.buffer = w.buffer[:0]
}

func (w *frameWriter) header(typ byte, size int) {
	if len(w.buffer)+frameHeaderLen > cap(w.buffer) {
		w.flush()
	}
	w.buffer = append(w.buffer, typ)
	// #nosec G115 -- a message body is bounded by the protocol's 1 GiB ceiling
	w.buffer = binary.BigEndian.AppendUint32(w.buffer, uint32(size+frameLenSize))
}

func (w *frameWriter) frame(typ byte, body []byte) {
	w.header(typ, len(body))
	if len(w.buffer)+len(body) <= cap(w.buffer) {
		w.buffer = append(w.buffer, body...)
		return
	}
	// a body that does not fit is written from where it lies instead of being copied
	w.flush()
	if w.err == nil && !writeAll(w.conn, body, w.timeout) {
		w.err = io.ErrClosedPipe
	}
}

func (w *frameWriter) stream(typ byte, from io.Reader, size int) {
	// forwards a message body of any size through the fixed buffer.
	w.header(typ, size)
	for size > 0 && w.err == nil {
		if len(w.buffer) == cap(w.buffer) {
			w.flush()
		}
		chunk := w.buffer[len(w.buffer):min(cap(w.buffer), len(w.buffer)+size)]
		n, err := io.ReadFull(from, chunk)
		w.buffer = w.buffer[:len(w.buffer)+n]
		size -= n
		if err != nil {
			w.err = err
		}
	}
	w.flush()
}

func (s *session) cancelFetch() {
	// asks the origin to stop a sub-query whose result is being discarded.
	ctx, cancel := context.WithTimeout(context.Background(), s.server.cancelBudget())
	defer cancel()
	_ = s.server.config.cancelUpstream(ctx, s.realPID, s.realSecret)
}

func trimNUL(body []byte) []byte {
	if n := len(body); n > 0 && body[n-1] == 0 {
		return body[:n-1]
	}
	return body
}
