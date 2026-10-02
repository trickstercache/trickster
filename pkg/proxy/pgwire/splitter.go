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
	"fmt"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
)

const (
	msgParse = 'P'
	// heldBufferKeepBytes is the largest reassembly buffer a session keeps.
	heldBufferKeepBytes = 64 * 1024
)

type clientSplitter struct {
	s       *session
	maxBody int
	maxHeld int
	forward func([]byte) error

	header    [frameHeaderLen]byte
	headerLen int
	remaining int
	held      []byte
	heldNeed  int
}

func (c *clientSplitter) atBoundary() bool {
	return c.headerLen == 0 && c.remaining == 0 && c.heldNeed == 0
}

func (c *clientSplitter) process(chunk []byte) error {
	spanStart, i := 0, 0
	flush := func(to int) error {
		if to > spanStart {
			if err := c.forward(chunk[spanStart:to]); err != nil {
				return err
			}
		}
		spanStart = to
		return nil
	}
	for i < len(chunk) {
		switch {
		case c.heldNeed > 0:
			n := min(c.heldNeed, len(chunk)-i)
			c.held = append(c.held, chunk[i:i+n]...)
			c.heldNeed -= n
			i += n
			spanStart = i
			if c.heldNeed == 0 {
				if err := c.release(c.held); err != nil {
					return err
				}
				c.resetHeld()
			}
		case c.remaining > 0:
			n := min(c.remaining, len(chunk)-i)
			c.remaining -= n
			i += n
		case c.headerLen == 0 && len(chunk)-i >= frameHeaderLen:
			// the common case: a header that lies whole inside the chunk
			typ, bodyLen, err := c.parseHeader(chunk[i : i+frameHeaderLen])
			if err != nil {
				return err
			}
			if !c.holds(typ, bodyLen) {
				c.s.countRequest(typ)
				c.remaining = bodyLen
				i += frameHeaderLen
				continue
			}
			if err = flush(i); err != nil {
				return err
			}
			end := i + frameHeaderLen + bodyLen
			if end <= len(chunk) {
				if err = c.release(chunk[i:end]); err != nil {
					return err
				}
				i = end
			} else {
				c.held = append(c.held[:0], chunk[i:]...)
				c.heldNeed = end - len(chunk)
				i = len(chunk)
			}
			spanStart = i
		default:
			// a header split across reads is set aside until its type is known
			if err := flush(i); err != nil {
				return err
			}
			n := copy(c.header[c.headerLen:], chunk[i:])
			c.headerLen += n
			i += n
			spanStart = i
			if c.headerLen < frameHeaderLen {
				continue
			}
			c.headerLen = 0
			typ, bodyLen, err := c.parseHeader(c.header[:])
			if err != nil {
				return err
			}
			if c.holds(typ, bodyLen) {
				c.held = append(c.held[:0], c.header[:]...)
				c.heldNeed = bodyLen
				if bodyLen == 0 {
					if err = c.release(c.held); err != nil {
						return err
					}
					c.resetHeld()
				}
				continue
			}
			c.s.countRequest(typ)
			c.remaining = bodyLen
			if err = c.forward(c.header[:]); err != nil {
				return err
			}
		}
	}
	return flush(len(chunk))
}

func (c *clientSplitter) parseHeader(header []byte) (byte, int, error) {
	length := binary.BigEndian.Uint32(header[1:])
	if length < frameLenSize || int64(length)-frameLenSize > int64(c.maxBody) {
		return 0, 0, fmt.Errorf("%w: type %q length %d", errFrameLength, header[0], length)
	}
	return header[0], int(length) - frameLenSize, nil
}

func (c *clientSplitter) holds(typ byte, bodyLen int) bool {
	// decides whether a message is read before it is relayed. A statement
	// too large to hold is relayed unread, which the session cannot then vouch for.
	switch typ {
	case msgQuery, msgParse:
		if bodyLen <= c.maxHeld {
			return true
		}
		c.s.tracker.disable(unsafeOversizedText)
		if typ == msgQuery {
			c.s.server.analysis.count(sqlanalyzer.CacheModeNone, reasonQuerySize)
		}
	case msgFunctionCall:
		// the fast-path interface can call any function, set_config included
		c.s.tracker.disable(unsafeFunctionCall)
	}
	return false
}

func (c *clientSplitter) release(message []byte) error {
	body := message[frameHeaderLen:]
	if message[0] == msgQuery {
		if outcome := c.s.gateQuery(body); outcome.eligible {
			// a statement answered from the cache is never sent to the origin
			if served, err := c.s.serveCached(outcome); served || err != nil {
				return err
			}
		}
	} else {
		c.s.observeParse(body)
	}
	c.s.countRequest(message[0])
	return c.forward(message)
}

func (c *clientSplitter) resetHeld() {
	if cap(c.held) > heldBufferKeepBytes {
		c.held = nil
		return
	}
	c.held = c.held[:0]
}
