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
	"bytes"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/snappy"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/encoding"
)

func TestSnappyCompressor(t *testing.T) {
	c := encoding.GetCompressor(storeAPICompressor)
	require.NotNil(t, c)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				// every message reuses pooled writers and readers, and must come back as it went
				msg := []byte(strings.Repeat(`{__name__="up",pod="`+strconv.Itoa(i*j)+`"}`, j+1))
				var buf bytes.Buffer
				w, err := c.Compress(&buf)
				require.NoError(t, err)
				_, err = w.Write(msg)
				require.NoError(t, err)
				require.NoError(t, w.Close())
				r, err := c.Decompress(&buf)
				require.NoError(t, err)
				got, err := io.ReadAll(r)
				require.NoError(t, err)
				require.Equal(t, msg, got)
			}
		})
	}
	wg.Wait()
	// the framing is snappy's stream format, which Thanos's compressor of the same name reads and writes
	var buf bytes.Buffer
	w, _ := c.Compress(&buf)
	_, _ = w.Write([]byte("labels"))
	require.NoError(t, w.Close())
	got, err := io.ReadAll(snappy.NewReader(&buf))
	require.NoError(t, err)
	require.Equal(t, "labels", string(got))
}
