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

package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/encoding/profile"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

type discardResponseWriter struct {
	h http.Header
}

func (d *discardResponseWriter) Header() http.Header         { return d.h }
func (d *discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardResponseWriter) WriteHeader(int)             {}

func benchJSONBody(size int) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"status":"success","data":{"resultType":"matrix","result":[`)
	for i := 0; buf.Len() < size; i++ {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(`{"metric":{"__name__":"up","job":"node","instance":"host-`)
		buf.WriteString(strconv.Itoa(i))
		buf.WriteString(`:9100"},"values":[[1700000000,"1"],[1700000015,"0.`)
		buf.WriteString(strconv.Itoa(i * 7919 % 1000))
		buf.WriteString(`"]]}`)
	}
	buf.WriteString(`]}}`)
	return buf.Bytes()
}

func BenchmarkHandleCompression(b *testing.B) {
	body := benchJSONBody(64 << 10)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
		w.Write(body)
	})
	h := HandleCompression(next, sets.New([]string{headers.ValueApplicationJSON}))
	for _, enc := range []string{providers.ZstandardValue, providers.BrotliValue, providers.GZipValue} {
		b.Run(enc, func(b *testing.B) {
			r := httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil)
			r.Header.Set(headers.NameAcceptEncoding, enc)
			w := &discardResponseWriter{h: make(http.Header)}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				clear(w.h)
				h.ServeHTTP(w, r)
			}
		})
	}
}

func BenchmarkHandleCompressionTranscode(b *testing.B) {
	body := benchJSONBody(64 << 10)
	for _, from := range []string{providers.ZstandardValue, providers.BrotliValue, providers.GZipValue} {
		ei, _ := providers.GetEncoderInitializer(from)
		var encoded bytes.Buffer
		ew := ei(&encoded, -1)
		ew.Write(body)
		ew.Close()
		stored := encoded.Bytes()
		next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// as the proxy engines do for an origin response that is already encoded
			profile.FromContext(r.Context()).ContentEncoding = from
			w.Header().Set(headers.NameContentType, headers.ValueApplicationJSON)
			w.Header().Set(headers.NameContentEncoding, from)
			w.Write(stored)
		})
		h := HandleCompression(next, sets.New([]string{headers.ValueApplicationJSON}))
		b.Run(from+"-to-identity", func(b *testing.B) {
			r := httptest.NewRequest(http.MethodGet, "http://"+appinfo.Domain+"/", nil)
			w := &discardResponseWriter{h: make(http.Header)}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				clear(w.h)
				h.ServeHTTP(w, r)
			}
		})
	}
}
