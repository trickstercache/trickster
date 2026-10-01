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
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	fso "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/providers"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"

	"github.com/stretchr/testify/require"
)

// keeps the sections of the documents it is given, for a test to look at
type sectionCache struct {
	*spyCache
	meta, body map[string][]byte
	err        error
	whole      bool
}

func newSectionCache() *sectionCache {
	return &sectionCache{spyCache: newSpyCache(), meta: map[string][]byte{}, body: map[string][]byte{}}
}

func (sc *sectionCache) SupportsSplit() bool { return !sc.whole }

func (sc *sectionCache) StoreSplit(key string, meta, body []byte, _ time.Duration) error {
	sc.meta[key], sc.body[key] = bytes.Clone(meta), bytes.Clone(body)
	return sc.err
}

func (sc *sectionCache) RetrieveSplit(key string) ([]byte, []byte, status.LookupStatus, error) {
	if sc.err != nil {
		return nil, nil, status.LookupStatusError, sc.err
	}
	if _, ok := sc.body[key]; !ok {
		return nil, nil, status.LookupStatusKeyMiss, cache.ErrKNF
	}
	return sc.meta[key], sc.body[key], status.LookupStatusHit, nil
}

func testDocument(body []byte) *HTTPDocument {
	return &HTTPDocument{
		StatusCode: 200, Status: "200 OK", ContentType: "application/json",
		ContentLength: int64(len(body)), Body: body,
		Headers: map[string][]string{"Content-Type": {"application/json"}, "Etag": {`"v1"`}},
		Ranges:  byterange.Ranges{{Start: 0, End: int64(len(body)) - 1}},
	}
}

func requireSameDocument(t *testing.T, want, got *HTTPDocument) {
	t.Helper()
	require.Equal(t, want.StatusCode, got.StatusCode)
	require.Equal(t, want.Status, got.Status)
	require.Equal(t, want.ContentType, got.ContentType)
	require.Equal(t, want.ContentLength, got.ContentLength)
	require.Equal(t, want.Headers, got.Headers)
	require.Equal(t, want.Ranges, got.Ranges)
	require.True(t, bytes.Equal(want.Body, got.Body))
}

func TestSections(t *testing.T) {
	compressible := []byte(strings.Repeat(`{"metric":"up","value":1},`, 400))
	tests := []struct {
		name       string
		body       []byte
		compress   bool
		compressed bool
	}{
		{"plain", compressible, false, false},
		{"compressed", compressible, true, true},
		{"too small to compress", compressible[:minCompressLen-1], true, false},
		{"just large enough to compress", compressible[:minCompressLen], true, true},
		{"no body", nil, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sc := newSectionCache()
			d := testDocument(test.body)
			require.NoError(t, writeConcurrent(context.Background(), sc, "k", d, test.compress, time.Minute))
			require.True(t, bytes.Equal(test.body, d.Body), "the document is stored, and is not changed")
			require.Empty(t, sc.stored, "the document is not also stored whole")

			if test.compressed {
				require.Equal(t, encodingFlag(cacheCodec), sc.meta["k"][0])
				require.Less(t, len(sc.body["k"]), len(test.body))
			} else {
				require.Zero(t, sc.meta["k"][0])
				require.True(t, bytes.Equal(test.body, sc.body["k"]), "the body is stored as it is")
			}
			require.Less(t, len(sc.meta["k"]), 256, "the body is no part of what describes it")

			qr := queryConcurrent(context.Background(), sc, "k")
			require.NoError(t, qr.err)
			require.Equal(t, status.LookupStatusHit, qr.lookupStatus)
			requireSameDocument(t, d, qr.d)
			if len(test.body) == 0 {
				require.Nil(t, qr.d.Body)
			}
		})
	}
}

func TestSectionsFailures(t *testing.T) {
	sc := newSectionCache()
	qr := queryConcurrent(context.Background(), sc, "absent")
	require.ErrorIs(t, qr.err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, qr.lookupStatus)

	sc.err = errTest
	require.ErrorIs(t, writeConcurrent(context.Background(), sc, "k", testDocument([]byte("b")), false, time.Minute), errTest)
	qr = queryConcurrent(context.Background(), sc, "k")
	require.ErrorIs(t, qr.err, errTest)
}

func TestDecodeSectionsRejects(t *testing.T) {
	sc := newSectionCache()
	body := []byte(strings.Repeat("compressible ", 100))
	require.NoError(t, writeConcurrent(context.Background(), sc, "plain", testDocument(body), false, time.Minute))
	require.NoError(t, writeConcurrent(context.Background(), sc, "packed", testDocument(body), true, time.Minute))
	plain, packed := sc.meta["plain"], sc.meta["packed"]
	_, n := binary.Uvarint(packed[flagLen:])
	claim := func(meta []byte, size uint64) []byte {
		out := binary.AppendUvarint([]byte{meta[0]}, size)
		return append(out, meta[flagLen+n:]...)
	}

	tests := []struct {
		name       string
		meta, body []byte
	}{
		{"no length", plain[:flagLen], body},
		{"length cut short", []byte{0, 0x80}, body},
		{"document cut short", plain[:len(plain)-1], body},
		{"body shorter than its length", plain, body[1:]},
		{"body longer than its length", plain, append(bytes.Clone(body), 'x')},
		{"compressed body is not", packed, body},
		{"compressed body cut short", packed, sc.body["packed"][:10]},
		{"decompresses to less than its length", claim(packed, uint64(len(body)+1)), sc.body["packed"]},
		{"decompresses to more than its length", claim(packed, uint64(len(body)-1)), sc.body["packed"]},
		{"length no body could decompress to", claim(packed, 1<<40), sc.body["packed"]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, decodeSections(&HTTPDocument{}, test.meta, test.body))
		})
	}
	require.NoError(t, decodeSections(&HTTPDocument{}, packed, sc.body["packed"]))
}

// a cache that keeps documents in sections still reads one that was stored whole
func TestSectionCacheReadsWholeDocument(t *testing.T) {
	for name, compress := range map[string]bool{"plain": false, "compressed": true} {
		t.Run(name, func(t *testing.T) {
			whole := newSectionCache()
			whole.whole = true
			d := testDocument([]byte(strings.Repeat("compressible ", 100)))
			require.NoError(t, writeConcurrent(context.Background(), whole, "k", d, compress, time.Minute))
			stored := bytes.Clone(whole.stored["k"])
			require.Equal(t, compress, stored[0] == encodingFlag(cacheCodec))
			qr := queryConcurrent(context.Background(), whole, "k")
			require.NoError(t, qr.err)
			requireSameDocument(t, d, qr.d)

			sc := newSectionCache()
			sc.meta["k"], sc.body["k"] = nil, stored
			qr = queryConcurrent(context.Background(), sc, "k")
			require.NoError(t, qr.err)
			requireSameDocument(t, d, qr.d)
		})
	}
	require.Error(t, decodeDocument(&HTTPDocument{}, []byte{encodingFlag(cacheCodec), 'x'}))
	require.Error(t, decodeDocument(&HTTPDocument{}, []byte{0, 'x'}))
	require.Error(t, decodeDocument(&HTTPDocument{}, nil))
}

func TestCompressBufferPool(t *testing.T) {
	large := bytes.NewBuffer(make([]byte, 0, maxPooledCompressBuffer+1))
	large.WriteString("kept out of the pool")
	putCompressBuffer(large)
	require.Equal(t, "kept out of the pool", large.String())
	small := bytes.NewBufferString("reset for the pool")
	putCompressBuffer(small)
	require.Zero(t, small.Len())
}

func newDiskCache(t testing.TB, provider string) cache.Cache {
	t.Helper()
	cfg := co.New()
	cfg.Name, cfg.Provider = "test", provider
	cfg.Filesystem = &fso.Options{CachePath: t.TempDir()}
	cfg.BBolt.Filename = t.TempDir() + "/test.db"
	c := cr.NewCache("test", cfg)
	t.Cleanup(func() { c.Close() })
	return c
}

func TestSectionsOnDisk(t *testing.T) {
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		for name, compress := range map[string]bool{"plain": false, "compressed": true} {
			t.Run(provider+"/"+name, func(t *testing.T) {
				c := newDiskCache(t, provider)
				_, ok := splitCache(c)
				require.True(t, ok)
				d := testDocument([]byte(strings.Repeat(`{"metric":"up","value":1},`, 400)))
				require.NoError(t, writeConcurrent(context.Background(), c, "k", d, compress, time.Minute))
				qr := queryConcurrent(context.Background(), c, "k")
				require.NoError(t, qr.err)
				require.Equal(t, status.LookupStatusHit, qr.lookupStatus)
				requireSameDocument(t, d, qr.d)
			})
		}
	}
}

func BenchmarkDocumentRoundTrip(b *testing.B) {
	body := []byte(strings.Repeat(`{"metric":"up","instance":"10.0.0.1:9090","value":"1.0"},`, 1<<12))
	for _, provider := range []string{providers.Filesystem, providers.BBolt} {
		for name, compress := range map[string]bool{"plain": false, "compressed": true} {
			c := newDiskCache(b, provider)
			d := testDocument(body)
			ctx := context.Background()
			if err := writeConcurrent(ctx, c, "k", d, compress, time.Hour); err != nil {
				b.Fatal(err)
			}
			if provider == providers.Filesystem {
				b.Run(provider+"/"+name+"/write", func(b *testing.B) {
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					for b.Loop() {
						writeConcurrent(ctx, c, "k", d, compress, time.Hour)
					}
				})
			}
			b.Run(provider+"/"+name+"/read", func(b *testing.B) {
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				for b.Loop() {
					if qr := queryConcurrent(ctx, c, "k"); qr.err != nil {
						b.Fatal(qr.err)
					}
				}
			})
		}
	}
}

func TestDecodeDocumentTakesTheBodyInPlace(t *testing.T) {
	// a document stored whole, as badger and redis keep one, is decoded from the buffer its read owns,
	// so its body is taken where it lies rather than copied
	d := testDocument([]byte(strings.Repeat("trickster ", 100)))
	b, err := d.MarshalMsg([]byte{0})
	require.NoError(t, err)
	got := &HTTPDocument{}
	require.NoError(t, decodeDocument(got, b))
	require.Equal(t, d.Body, got.Body)
	start := uintptr(unsafe.Pointer(&b[0]))
	at := uintptr(unsafe.Pointer(&got.Body[0]))
	require.True(t, at > start && at < start+uintptr(len(b)), "the body was copied out of the read's buffer")

	empty := testDocument(nil)
	b, err = empty.MarshalMsg([]byte{0})
	require.NoError(t, err)
	got = &HTTPDocument{}
	require.NoError(t, decodeDocument(got, b))
	require.Nil(t, got.Body, "an empty body decodes as none")
}
