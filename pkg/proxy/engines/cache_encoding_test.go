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

package engines

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/encoding/handler"
	encodings "github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
)

const encodedContentType = "application/json"

// what a client is sent for a compressible object, through the compression middleware as a
// backend's routes are
func encodedResponse(t *testing.T, h *streamHarness, hdrs map[string]string) streamResponse {
	t.Helper()
	// each request has its own resources, which record that the middleware has handled it
	r, err := request.Clone(h.r)
	require.NoError(t, err)
	for k, v := range hdrs {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.HandleCompression(http.HandlerFunc(ObjectProxyCacheRequest),
		sets.New([]string{encodedContentType})).ServeHTTP(w, r)
	resp := w.Result()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return streamResponse{code: resp.StatusCode, body: string(b), header: resp.Header.Clone()}
}

func newEncodedHarness(t *testing.T) *streamHarness {
	t.Helper()
	ts, _, r, rsc, err := setupTestHarnessOPC("", streamBody(), http.StatusOK, map[string]string{
		headers.NameCacheControl: "max-age=60", headers.NameContentType: encodedContentType,
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeTestHarness(ts, r) })
	c := &countingCache{Cache: newDiskCache(t, providers.Filesystem)}
	rsc.CacheClient, rsc.CacheConfig = c, c.Configuration()
	return &streamHarness{t: t, r: r, rsc: rsc, cache: c}
}

func decoded(t *testing.T, enc encodings.Provider, body string) string {
	t.Helper()
	b, err := decodeAll(enc, []byte(body), 0)
	require.NoError(t, err)
	return string(b)
}

func TestCompressedObjectServedAsStored(t *testing.T) {
	// a hit on an object the cache stored compressed is sent to a client that accepts the codec just as
	// it was stored, and decoded for any other
	for _, codec := range []encodings.Provider{encodings.Brotli, encodings.GZip, encodings.Zstandard} {
		t.Run(codec.String(), func(t *testing.T) {
			defer func(prev encodings.Provider) { cacheCodec = prev }(cacheCodec)
			cacheCodec = codec
			h := newEncodedHarness(t)
			accept := map[string]string{headers.NameAcceptEncoding: codec.String()}
			var stored bytes.Buffer
			require.NoError(t, encodeTo(&stored, []byte(streamBody())))

			miss := encodedResponse(t, h, accept)
			require.Equal(t, streamBody(), decoded(t, codec, miss.body))

			hit := encodedResponse(t, h, accept)
			require.Equal(t, http.StatusOK, hit.code)
			require.Equal(t, codec.String(), hit.header.Get(headers.NameContentEncoding))
			require.Equal(t, stored.String(), hit.body, "the body is sent as the cache stored it")
			require.Contains(t, hit.header.Values(headers.NameVary), headers.NameAcceptEncoding)
			require.NotNil(t, h.cache.last(), "the body was streamed from the cache")

			// a client that takes no encoding is sent the object decoded
			plain := encodedResponse(t, h, nil)
			require.Equal(t, streamBody(), plain.body)
			require.Empty(t, plain.header.Get(headers.NameContentEncoding))

			// ranges count the decoded object
			part := encodedResponse(t, h, map[string]string{
				headers.NameAcceptEncoding: codec.String(), headers.NameRange: "bytes=100-199",
			})
			require.Equal(t, http.StatusPartialContent, part.code)
			body := part.body
			if ce := part.header.Get(headers.NameContentEncoding); ce != "" {
				body = decoded(t, encodings.ProviderID(ce), body)
			}
			require.Equal(t, streamBody()[100:200], body)
			h.cache.requireAllClosed(t)
		})
	}
}

func TestCompressedObjectForAnotherCodec(t *testing.T) {
	// a client that accepts another codec than the one the object was stored with is sent it decoded,
	// and encoded anew by the middleware
	h := newEncodedHarness(t)
	encodedResponse(t, h, nil)
	other := encodings.GZip
	if cacheCodec == other {
		other = encodings.Brotli
	}
	hit := encodedResponse(t, h, map[string]string{headers.NameAcceptEncoding: other.String()})
	require.Equal(t, other.String(), hit.header.Get(headers.NameContentEncoding))
	require.Equal(t, streamBody(), decoded(t, other, hit.body))
	h.cache.requireAllClosed(t)
}

func TestEncodingFlags(t *testing.T) {
	for _, enc := range []encodings.Provider{
		encodings.Identity, encodings.Zstandard, encodings.Brotli,
		encodings.GZip, encodings.Deflate,
	} {
		got, ok := flagEncoding(encodingFlag(enc))
		require.True(t, ok, enc.String())
		require.Equal(t, enc, got)
	}
	// what a brotli-compressed object was marked with before its codec was recorded
	got, ok := flagEncoding(flagLegacyCompressed)
	require.True(t, ok)
	require.Equal(t, encodings.Brotli, got)
	for _, flag := range []byte{2, flagEncodedBit, flagEncodedBit | 0x3, flagEncodedBit | 0x40} {
		_, ok := flagEncoding(flag)
		require.False(t, ok, "flag %#x", flag)
	}
}

func TestLegacyCompressedSectionsDecode(t *testing.T) {
	// an object stored before the codec was recorded still decodes
	defer func(prev encodings.Provider) { cacheCodec = prev }(cacheCodec)
	cacheCodec = encodings.Brotli
	body := bytes.Repeat([]byte("trickster "), 200)
	var buf bytes.Buffer
	require.NoError(t, encodeTo(&buf, body))
	d := testDocument(body)
	m := d.ShallowCopy()
	m.Body = nil
	meta, err := m.MarshalMsg(append([]byte{flagLegacyCompressed}, byte(len(body)&0x7f|0x80), byte(len(body)>>7)))
	require.NoError(t, err)
	got := &HTTPDocument{}
	require.NoError(t, decodeSections(got, meta, buf.Bytes()))
	require.Equal(t, body, got.Body)
}

func TestDeferredCompressedBodyReadsDecoded(t *testing.T) {
	// a compressed body left in the cache is decoded when read whole, as a stale object is to
	// revalidate, and read whole for a range, as ranges count the decoded body
	want := []byte(streamBody())
	var stored bytes.Buffer
	require.NoError(t, encodeTo(&stored, want))
	deferredDoc := func() *HTTPDocument {
		return &HTTPDocument{
			ContentLength: int64(len(want)), storedEncoding: cacheCodec, storedSize: uint64(len(want)),
			deferred: &bytesBody{Reader: bytes.NewReader(stored.Bytes())},
		}
	}
	d := deferredDoc()
	require.NoError(t, d.materialize())
	require.Equal(t, want, d.Body)
	require.Equal(t, encodings.Identity, d.storedEncoding)

	d = deferredDoc()
	parts, err := d.readRanges(byterange.Ranges{{Start: 100, End: 199}, {Start: 4000, End: 4099}})
	require.NoError(t, err)
	require.Equal(t, want[100:200], parts[byterange.Range{Start: 100, End: 199}].Content)
	require.Equal(t, want[4000:4100], parts[byterange.Range{Start: 4000, End: 4099}].Content)
	_, err = deferredDoc().readRanges(byterange.Ranges{{Start: 0, End: int64(len(want))}})
	require.Error(t, err, "a range past the decoded body")

	// a body that doesn't decode to what the meta section said
	d = deferredDoc()
	d.storedSize++
	require.Error(t, d.materialize())
}

func TestDecodeRejectsDamagedStreams(t *testing.T) {
	// a stream damaged after its last byte of output is rejected however the body is decoded, even
	// when the meta section gives the length it decodes to
	want := []byte(streamBody())
	encode := func(enc encodings.Provider) []byte {
		var buf bytes.Buffer
		ei, _ := encodings.SelectEncoderInitializer(enc)
		w := ei(&buf, cacheCompressionLevel)
		_, err := w.Write(want)
		require.NoError(t, err)
		require.NoError(t, w.Close())
		return buf.Bytes()
	}
	damage := func(b []byte, f func([]byte) []byte) []byte { return f(bytes.Clone(b)) }
	flip := func(at int) func([]byte) []byte {
		return func(b []byte) []byte { b[len(b)-at] ^= 0xff; return b }
	}
	cut := func(n int) func([]byte) []byte { return func(b []byte) []byte { return b[:len(b)-n] } }
	gz, zs, br := encode(encodings.GZip), encode(encodings.Zstandard), encode(encodings.Brotli)
	tests := []struct {
		name string
		enc  encodings.Provider
		body []byte
		size int
	}{
		{"gzip bad crc", encodings.GZip, damage(gz, flip(8)), len(want)},
		{"gzip bad length", encodings.GZip, damage(gz, flip(1)), len(want)},
		{"gzip truncated trailer", encodings.GZip, damage(gz, cut(4)), len(want)},
		{"zstd bad checksum", encodings.Zstandard, damage(zs, flip(1)), len(want)},
		{"zstd truncated checksum", encodings.Zstandard, damage(zs, cut(2)), len(want)},
		{"brotli truncated end", encodings.Brotli, damage(br, cut(1)), len(want)},
		{"gzip extra decoded byte", encodings.GZip, gz, len(want) - 1},
		{"zstd extra decoded byte", encodings.Zstandard, zs, len(want) - 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeAll(test.enc, test.body, test.size)
			require.Error(t, err, "sized")
			if test.size == len(want) {
				_, err = decodeAll(test.enc, test.body, 0)
				require.Error(t, err, "unsized")
			}
			meta, err := (&HTTPDocument{}).MarshalMsg(binary.AppendUvarint(
				[]byte{encodingFlag(test.enc)}, uint64(test.size)))
			require.NoError(t, err)
			require.Error(t, decodeSections(&HTTPDocument{}, meta, test.body))
			d := &HTTPDocument{
				storedEncoding: test.enc, storedSize: uint64(test.size),
				deferred: &bytesBody{Reader: bytes.NewReader(test.body)},
			}
			require.Error(t, d.materialize())
		})
	}
	for enc, body := range map[encodings.Provider][]byte{
		encodings.GZip: gz, encodings.Zstandard: zs, encodings.Brotli: br,
		encodings.Deflate: encode(encodings.Deflate),
	} {
		for _, size := range []int{len(want), 0} {
			got, err := decodeAll(enc, body, size)
			require.NoError(t, err, "%v, size %d", enc, size)
			require.Equal(t, want, got)
		}
	}
}

type bytesBody struct {
	*bytes.Reader
}

func (b *bytesBody) Close() error { return nil }

func (b *bytesBody) ReadAll() ([]byte, error) {
	out := make([]byte, b.Size())
	_, err := b.ReadAt(out, 0)
	return out, err
}

func BenchmarkCompressedCacheHit(b *testing.B) {
	// a hit on a compressible object the cache stored compressed, through the compression middleware,
	// for clients that accept the cache's codec, another, or none
	const size = 256 << 10
	var sb strings.Builder
	for i := 0; sb.Len() < size; i++ {
		sb.WriteString(`{"metric":{"__name__":"up","instance":"host-` + strconv.Itoa(i) + `:9100"},"value":[1700000000,"1"]},`)
	}
	body := sb.String()[:size]
	ts, _, r, rsc, err := setupTestHarnessOPC("", body, http.StatusOK, map[string]string{
		headers.NameCacheControl: "max-age=600", headers.NameContentType: encodedContentType,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { closeTestHarness(ts, r) })
	c := newDiskCache(b, providers.Filesystem)
	rsc.CacheClient, rsc.CacheConfig = c, c.Configuration()
	rsc.Tracer = nil
	h := handler.HandleCompression(http.HandlerFunc(ObjectProxyCacheRequest), sets.New([]string{encodedContentType}))
	serve := func(accept string) *discardWriter {
		req, _ := request.Clone(r)
		if accept != "" {
			req.Header.Set(headers.NameAcceptEncoding, accept)
		}
		w := &discardWriter{header: http.Header{}}
		h.ServeHTTP(w, req)
		return w
	}
	serve("")
	for _, accept := range []string{cacheCodec.String(), encodings.GZipValue, ""} {
		name := accept
		if name == "" {
			name = "identity"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				serve(accept)
			}
		})
	}
}
