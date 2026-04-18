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

// Package server implements the server side of the ClickHouse native TCP
// wire protocol. It speaks enough of the protocol to accept connections from
// clickhouse-go (or the ClickHouse CLI) and proxy queries through Trickster's
// caching engine.
package server

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// Client-to-server packet types.
const (
	ClientHello  = 0
	ClientQuery  = 1
	ClientData   = 2
	ClientCancel = 3
	ClientPing   = 4
)

// Server-to-client packet types.
const (
	ServerHello       = 0
	ServerData        = 1
	ServerException   = 2
	ServerProgress    = 3
	ServerPong        = 4
	ServerEndOfStream = 5
	ServerProfileInfo = 6
)

// Protocol revision thresholds used by the server handshake and query parsing.
const (
	RevisionTimezone            = 54058
	RevisionQuotaKey            = 54060
	RevisionDisplayName         = 54372
	RevisionVersionPatch        = 54401
	RevisionClientWriteInfo     = 54420
	RevisionInterserverSecret   = 54441
	RevisionOpenTelemetry       = 54442
	RevisionDistributedDepth    = 54448
	RevisionInitialQueryStart   = 54449
	RevisionParallelReplicas    = 54453
	RevisionCustomSerialization = 54454
	RevisionParameters          = 54459
	RevisionAddendum            = 54458
	RevisionServerQueryTime     = 54460
)

// ServerRevision is the protocol revision we advertise to clients.
// This matches the DBMS_TCP_PROTOCOL_VERSION from clickhouse-go.
const ServerRevision = RevisionServerQueryTime

// ---------- wire primitives ----------

type protoReader struct {
	*bufio.Reader
}

func newProtoReader(r io.Reader) *protoReader {
	return &protoReader{bufio.NewReaderSize(r, 128*1024)}
}

func (r *protoReader) uvarint() (uint64, error) {
	return binary.ReadUvarint(r)
}

func (r *protoReader) str() (string, error) {
	n, err := r.uvarint()
	if err != nil {
		return "", err
	}
	if n > 10*1024*1024 {
		return "", fmt.Errorf("string too long: %d bytes", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", err
	}
	return string(b), nil
}

func (r *protoReader) int32() (int32, error) {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b[:])), nil //nolint:gosec // wire protocol reinterpret
}

func (r *protoReader) boolean() (bool, error) {
	b, err := r.ReadByte()
	return b != 0, err
}

type protoWriter struct {
	io.Writer
	buf [binary.MaxVarintLen64]byte
}

func newProtoWriter(w io.Writer) *protoWriter {
	return &protoWriter{Writer: w}
}

func (w *protoWriter) putByte(b byte) error {
	w.buf[0] = b
	_, err := w.Write(w.buf[:1])
	return err
}

func (w *protoWriter) putUvarint(v uint64) error {
	n := binary.PutUvarint(w.buf[:], v)
	_, err := w.Write(w.buf[:n])
	return err
}

func (w *protoWriter) putStr(s string) error {
	if err := w.putUvarint(uint64(len(s))); err != nil {
		return err
	}
	if len(s) > 0 {
		_, err := w.Write([]byte(s))
		return err
	}
	return nil
}

func (w *protoWriter) putInt32(v int32) error {
	binary.LittleEndian.PutUint32(w.buf[:4], uint32(v)) //nolint:gosec // wire protocol reinterpret
	_, err := w.Write(w.buf[:4])
	return err
}

func (w *protoWriter) putInt64(v int64) error {
	binary.LittleEndian.PutUint64(w.buf[:8], uint64(v)) //nolint:gosec // wire protocol reinterpret
	_, err := w.Write(w.buf[:8])
	return err
}

func (w *protoWriter) putFloat64(v float64) error {
	return w.putInt64(int64(math.Float64bits(v))) //nolint:gosec // IEEE 754 bit cast
}

func (w *protoWriter) putBool(v bool) error {
	if v {
		return w.putByte(1)
	}
	return w.putByte(0)
}
