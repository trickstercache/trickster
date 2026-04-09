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

	"github.com/trickstercache/trickster/v2/pkg/encoding/brotli"
	"github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zlib"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zstd"
)

// IdentityContentEncoding is the HTTP Content-Encoding token meaning no transformation.
const IdentityContentEncoding = "identity"

const maxContentEncodingStack = 8

// ceKind identifies a Content-Encoding token we can decode (stack order = application order).
type ceKind byte

const (
	ceGzip ceKind = iota + 1
	ceZstd
	ceDeflate
	ceBrotli
)

func ceFieldIsSpace(b byte) bool { return b == ' ' || b == '\t' }

// ceFieldEqualFoldASCII compares field to lowerASCII (must be lower case).
func ceFieldEqualFoldASCII(field, lowerASCII string) bool {
	if len(field) != len(lowerASCII) {
		return false
	}
	for i := 0; i < len(field); i++ {
		c := field[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != lowerASCII[i] {
			return false
		}
	}
	return true
}

func classifyContentEncodingField(field string) (ceKind, bool) {
	switch len(field) {
	case len(providers.BrotliValue):
		if ceFieldEqualFoldASCII(field, providers.BrotliValue) {
			return ceBrotli, true
		}
	case len(providers.GZipValue):
		if ceFieldEqualFoldASCII(field, providers.GZipValue) {
			return ceGzip, true
		}
		if ceFieldEqualFoldASCII(field, providers.ZstandardValue) {
			return ceZstd, true
		}
	case len(providers.GZipAltValue):
		if ceFieldEqualFoldASCII(field, providers.GZipAltValue) {
			return ceGzip, true
		}
	case len(providers.DeflateValue):
		if ceFieldEqualFoldASCII(field, providers.DeflateValue) {
			return ceDeflate, true
		}
	}
	return 0, false
}

// scanContentEncodings walks ce in a single pass. Codings are recorded
// in application order; the caller decodes last-applied first.
func scanContentEncodings(ce string,
	stack *[maxContentEncodingStack]ceKind) (n int, err error) {
	i := 0
	for i < len(ce) {
		for i < len(ce) && (ce[i] == ',' || ceFieldIsSpace(ce[i])) {
			i++
		}
		if i >= len(ce) {
			break
		}
		j := i
		for j < len(ce) && ce[j] != ',' {
			j++
		}
		start, end := i, j
		for start < end && ceFieldIsSpace(ce[start]) {
			start++
		}
		for end > start && ceFieldIsSpace(ce[end-1]) {
			end--
		}
		for k := start; k < end; k++ {
			if ce[k] == ';' {
				end = k
				for end > start && ceFieldIsSpace(ce[end-1]) {
					end--
				}
				break
			}
		}
		if start < end {
			field := ce[start:end]
			if ceFieldEqualFoldASCII(field, IdentityContentEncoding) {
				i = j
				if j < len(ce) && ce[j] == ',' {
					i++
				}
				continue
			}
			kind, ok := classifyContentEncodingField(field)
			if !ok {
				return 0, fmt.Errorf("unsupported content-encoding %q", field)
			}
			if n >= maxContentEncodingStack {
				return 0, fmt.Errorf("too many content-encoding layers")
			}
			stack[n] = kind
			n++
		}
		i = j
		if j < len(ce) && ce[j] == ',' {
			i++
		}
	}
	return n, nil
}

// DecompressResponseBody removes Content-Encoding layers from body using the raw
// Content-Encoding header value (for example http.Header.Get). An empty ce returns
// body unchanged. Codings are applied in reverse listed order per RFC 9110.
func DecompressResponseBody(ce string, body []byte) ([]byte, error) {
	if ce == "" {
		return body, nil
	}
	var stack [maxContentEncodingStack]ceKind
	n, err := scanContentEncodings(ce, &stack)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return body, nil
	}
	for k := n - 1; k >= 0; k-- {
		switch stack[k] {
		case ceGzip:
			body, err = gzip.Decode(body)
		case ceZstd:
			body, err = zstd.Decode(body)
		case ceDeflate:
			body, err = zlib.Decode(body)
		case ceBrotli:
			body, err = brotli.Decode(body)
		default:
			err = fmt.Errorf("internal content-encoding kind %d", stack[k])
		}
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}
