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

package global

import (
	"errors"
	"io"
	"sync"

	"github.com/klauspost/compress/snappy"
	"google.golang.org/grpc/encoding"
)

// storeAPICompressor is the gRPC compressor StoreAPI calls ask for, as Thanos's servers register it
const storeAPICompressor = "snappy"

func init() {
	encoding.RegisterCompressor(&snappyCompressor{})
}

// snappyCompressor compresses gRPC messages in the snappy framing format, as Thanos's StoreAPI servers do
// under the same name; writers and readers are pooled, as each message takes one
type snappyCompressor struct {
	writers, readers sync.Pool
}

func (*snappyCompressor) Name() string { return storeAPICompressor }

func (c *snappyCompressor) Compress(w io.Writer) (io.WriteCloser, error) {
	if sw, ok := c.writers.Get().(*snappyWriter); ok {
		sw.Reset(w)
		return sw, nil
	}
	return &snappyWriter{Writer: snappy.NewBufferedWriter(w), pool: &c.writers}, nil
}

func (c *snappyCompressor) Decompress(r io.Reader) (io.Reader, error) {
	if sr, ok := c.readers.Get().(*snappyReader); ok {
		sr.Reset(r)
		return sr, nil
	}
	return &snappyReader{Reader: snappy.NewReader(r), pool: &c.readers}, nil
}

type snappyWriter struct {
	*snappy.Writer
	pool *sync.Pool
}

func (w *snappyWriter) Close() error {
	err := w.Writer.Close()
	w.pool.Put(w)
	return err
}

type snappyReader struct {
	*snappy.Reader
	pool *sync.Pool
}

// Read returns the reader to its pool at the end of its message, which gRPC reads only up to
func (r *snappyReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.pool.Put(r)
	}
	return n, err
}
