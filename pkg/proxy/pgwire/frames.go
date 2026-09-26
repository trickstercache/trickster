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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// frameHeaderLen is a message's type byte plus its int32 length.
	frameHeaderLen = 5
	// frameLenSize is the length field, which counts itself but not the type byte.
	frameLenSize = 4

	// Frontend message types the relay acts on.
	msgQuery        = 'Q'
	msgSync         = 'S'
	msgFunctionCall = 'F'
	msgTerminate    = 'X'
	msgPassword     = 'p'

	// Backend message types the relay acts on.
	msgAuthentication  = 'R'
	msgBackendKeyData  = 'K'
	msgDataRow         = 'D'
	msgErrorResponse   = 'E'
	msgReadyForQuery   = 'Z'
	msgParameterStatus = 'S'
)

var errFrameLength = errors.New("invalid message length")

type frameObserver interface {
	message(typ byte, bodyLen int) bool
	firstByte(typ, b byte)
	body(typ byte, body []byte)
}

// maxCapturedBody bounds the bodies a scanner copies for its observer.
const maxCapturedBody = 64 * 1024

type frameScanner struct {
	observer  frameObserver
	maxBody   int
	header    [frameHeaderLen]byte
	headerLen int
	remaining int
	typ       byte
	wantFirst bool
	capturing bool
	captured  []byte
}

func newFrameScanner(observer frameObserver, maxBody int) *frameScanner {
	return &frameScanner{observer: observer, maxBody: maxBody}
}

func (s *frameScanner) atBoundary() bool {
	return s.remaining == 0 && s.headerLen == 0
}

func (s *frameScanner) scan(chunk []byte) error {
	for len(chunk) > 0 {
		if s.remaining > 0 {
			if s.wantFirst {
				s.wantFirst = false
				s.observer.firstByte(s.typ, chunk[0])
			}
			n := min(s.remaining, len(chunk))
			s.remaining -= n
			if s.capturing {
				s.captured = append(s.captured, chunk[:n]...)
				if s.remaining == 0 {
					s.capturing = false
					s.observer.body(s.typ, s.captured)
				}
			}
			chunk = chunk[n:]
			continue
		}
		n := copy(s.header[s.headerLen:], chunk)
		s.headerLen += n
		chunk = chunk[n:]
		if s.headerLen < frameHeaderLen {
			return nil
		}
		s.headerLen = 0
		length := binary.BigEndian.Uint32(s.header[1:])
		if length < frameLenSize || int64(length)-frameLenSize > int64(s.maxBody) {
			return fmt.Errorf("%w: type %q length %d", errFrameLength, s.header[0], length)
		}
		s.typ = s.header[0]
		s.remaining = int(length) - frameLenSize
		s.wantFirst = s.typ == msgReadyForQuery && s.remaining > 0
		if s.observer.message(s.typ, s.remaining) && s.remaining > 0 && s.remaining <= maxCapturedBody {
			s.capturing, s.captured = true, s.captured[:0]
		}
	}
	return nil
}

func readFrame(r io.Reader, maxBody int) (typ byte, body []byte, err error) {
	// reads exactly one typed message, without reading past its end.
	var header [frameHeaderLen]byte
	if _, err = io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length < frameLenSize || int64(length)-frameLenSize > int64(maxBody) {
		return 0, nil, fmt.Errorf("%w: type %q length %d", errFrameLength, header[0], length)
	}
	body = make([]byte, length-frameLenSize)
	if _, err = io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return header[0], body, nil
}

func appendFrame(dst []byte, typ byte, body []byte) []byte {
	dst = append(dst, typ)
	// #nosec G115 -- callers only pass bodies bounded by the protocol ceiling
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(body)+frameLenSize))
	return append(dst, body...)
}

// pgMaxMessageBody is the largest body PostgreSQL itself will send.
const pgMaxMessageBody = 0x3fffffff
