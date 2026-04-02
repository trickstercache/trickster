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

package prometheus

import (
	"bytes"
	"compress/gzip"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
)

var testLogger = logging.NoopLogger()

func TestProcessTransformations(t *testing.T) {
	// passing test case is no panics
	c := &Client{injectLabels: map[string]string{"test": "trickster"}}
	c.ProcessTransformations(nil)
	c.hasTransformations = true
	c.ProcessTransformations(nil)
	c.ProcessTransformations(&dataset.DataSet{})
}

func TestDefaultWrite(t *testing.T) {
	w := httptest.NewRecorder()
	defaultWrite(200, w, []byte("trickster"))
	if w.Body.String() != "trickster" || w.Code != 200 {
		t.Error("write mismatch")
	}
}

func TestProcessVectorTransformations(t *testing.T) {
	logger.SetLogger(testLogger)
	c := &Client{}
	w := httptest.NewRecorder()

	rsc := &request.Resources{}
	body := []byte("trickster")
	statusCode := 200
	c.processVectorTransformations(w, body, statusCode, rsc)
	if w.Code != 200 {
		t.Errorf("expected %d got %d", 200, w.Code)
	}
}

func testGzipCompress(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDecompressGzip(t *testing.T) {
	t.Run("plain JSON returned unchanged", func(t *testing.T) {
		input := []byte(`{"status":"ok"}`)
		got := decompressGzip(input)
		if !bytes.Equal(got, input) {
			t.Errorf("expected input unchanged, got %q", got)
		}
	})

	t.Run("gzip-compressed JSON returned decompressed", func(t *testing.T) {
		want := []byte(`{"status":"ok"}`)
		compressed := testGzipCompress(t, want)
		got := decompressGzip(compressed)
		if !bytes.Equal(got, want) {
			t.Errorf("expected %q, got %q", want, got)
		}
	})

	t.Run("empty input returned unchanged", func(t *testing.T) {
		input := []byte{}
		got := decompressGzip(input)
		if !bytes.Equal(got, input) {
			t.Errorf("expected empty slice, got %q", got)
		}
	})

	t.Run("truncated gzip returned unchanged", func(t *testing.T) {
		input := []byte{0x1f, 0x8b}
		got := decompressGzip(input)
		if !bytes.Equal(got, input) {
			t.Errorf("expected input unchanged, got %q", got)
		}
	})
}
