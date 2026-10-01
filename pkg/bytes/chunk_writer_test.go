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

package bytes

import (
	"bytes"
	"errors"
	"testing"
)

type countingWriter struct {
	bytes.Buffer
	writes int
	failAt int
}

var errWriteFailed = errors.New("write failed")

func (w *countingWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.failAt > 0 && w.writes >= w.failAt {
		return 0, errWriteFailed
	}
	return w.Buffer.Write(b)
}

func TestChunkWriterWritesInParts(t *testing.T) {
	w := &countingWriter{}
	cw := NewChunkWriter(w)
	for range 3 * ChunkSize / 8 {
		cw.Buf = append(cw.Buf, "12345678"...)
		cw.FlushIfFull()
	}
	if err := cw.Close(); err != nil || w.Len() != 3*ChunkSize || w.writes != 3 {
		t.Fatalf("wrote %d bytes in %d writes: %v", w.Len(), w.writes, err)
	}
	if cw.Buf != nil || cw.bp != nil {
		t.Fatal("a closed writer kept its buffer")
	}
}

func TestChunkWriterStopsAtFailedWrite(t *testing.T) {
	w := &countingWriter{failAt: 2}
	cw := NewChunkWriter(w)
	for range 3 {
		cw.Buf = append(cw.Buf, make([]byte, ChunkSize)...)
		cw.FlushIfFull()
	}
	if !errors.Is(cw.Err(), errWriteFailed) || w.writes != 2 {
		t.Fatalf("err %v after %d writes", cw.Err(), w.writes)
	}
	if err := cw.Close(); !errors.Is(err, errWriteFailed) || w.writes != 2 {
		t.Fatalf("close = %v after %d writes", err, w.writes)
	}
}

func TestChunkWriterDropsLargeBuffers(t *testing.T) {
	cw := NewChunkWriter(&countingWriter{})
	bp := cw.bp
	cw.Buf = append(cw.Buf, make([]byte, maxPooledChunkBuffer+1)...)
	if err := cw.Close(); err != nil || cw.bp != nil {
		t.Fatal(err)
	}
	if cap(*bp) > maxPooledChunkBuffer {
		t.Fatal("the grown buffer was stored back")
	}
}
