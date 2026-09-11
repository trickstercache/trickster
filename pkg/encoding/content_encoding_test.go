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

package encoding

import (
	"fmt"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/encoding/brotli"
	"github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zlib"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zstd"
)

func TestDecompressResponseBody(t *testing.T) {
	jsonBytes := []byte(`{"status":"success"}`)

	t.Run("plain no header", func(t *testing.T) {
		out, err := DecompressResponseBody("", jsonBytes)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("identity only header", func(t *testing.T) {
		out, err := DecompressResponseBody(IdentityContentEncoding, jsonBytes)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("gzip header with q and identity before zstd", func(t *testing.T) {
		// Listed order = application order: gzip(JSON), then zstd(·); decode outer zstd first.
		gz, err := gzip.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		zb, err := zstd.Encode(gz)
		if err != nil {
			t.Fatal(err)
		}
		ce := fmt.Sprintf("%s;q=1.0, %s , %s", providers.GZipValue, IdentityContentEncoding, providers.Zstandard)
		out, err := DecompressResponseBody(ce, zb)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("gzip header", func(t *testing.T) {
		gz, err := gzip.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecompressResponseBody(providers.GZipValue, gz)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("x-gzip header", func(t *testing.T) {
		gz, err := gzip.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecompressResponseBody(providers.GZipAltValue, gz)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("br header", func(t *testing.T) {
		br, err := brotli.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecompressResponseBody(providers.BrotliValue, br)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("empty content-encoding unchanged", func(t *testing.T) {
		gz, err := gzip.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecompressResponseBody("", gz)
		if err != nil || string(out) != string(gz) {
			t.Fatalf("expected raw gzip bytes when header absent, out=%q err=%v", out, err)
		}
	})

	t.Run("zstd header", func(t *testing.T) {
		zb, err := zstd.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecompressResponseBody(providers.ZstandardValue, zb)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("deflate header zlib body", func(t *testing.T) {
		zb, err := zlib.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		out, err := DecompressResponseBody(providers.DeflateValue, zb)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("chained zstd then gzip", func(t *testing.T) {
		zb, err := zstd.Encode(jsonBytes)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.Encode(zb)
		if err != nil {
			t.Fatal(err)
		}
		ce := fmt.Sprintf("%s, %s", providers.Zstandard, providers.GZipValue)
		out, err := DecompressResponseBody(ce, gz)
		if err != nil || string(out) != string(jsonBytes) {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})

	t.Run("unsupported coding", func(t *testing.T) {
		_, err := DecompressResponseBody("compress", []byte("x"))
		if err == nil {
			t.Fatal("expected error")
		}
	})
}
