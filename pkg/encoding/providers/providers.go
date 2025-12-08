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

package providers

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

const (
	Zstandard Provider = 1 << iota
	Brotli                 // 2
	GZip                   // 4
	Deflate                // 8
	Identity  Provider = 0 // no encoding
	// capacity for 3 more encoding types @ 16, 32, 64
	//
	// browsers don't currently support snappy, so it is isolated to ensure a full
	// bifurcation of web vs. general encoders, as more providers are added later
	Snappy Provider = 128

	maxWebProvider = Deflate // update whenever another web-compatible provider is added

	// for use in headers
	ZstandardValue = "zstd"
	BrotliValue    = "br"
	GZipValue      = "gzip"
	DeflateValue   = "deflate"
	SnappyValue    = "snappy"
	// might be used in configs
	ZstandardAltValue = "zstandard"
	BrotliAltValue    = "brotli"
)

type (
	Provider      byte
	Lookup        map[string]Provider
	ReverseLookup map[Provider]string
)

// Update whenever a new encoder provider is added
var providerVals = []Provider{1, 2, 4, 8, 128}

// Update whenever a new encoder provider is added
var providerValLookup = ReverseLookup{
	Zstandard: ZstandardValue,
	Brotli:    BrotliValue,
	GZip:      GZipValue,
	Deflate:   DeflateValue,
	Snappy:    SnappyValue,
}

// these are populated in init based on maxWebProvider, providerVals, and providerValLookup
var (
	providers                      []string
	webProviders                   []string
	providerLookup                 Lookup
	webProviderLookup              Lookup
	webValLookup                   ReverseLookup
	webProviderVals                []Provider
	AllSupportedWebProviders       string
	AllSupportedWebProvidersBitmap Provider
)

func init() {
	l := len(providerVals)
	webProviderVals = make([]Provider, 0, l)
	providers = make([]string, 0, l)
	webProviders = make([]string, 0, l)
	webValLookup = make(ReverseLookup)
	providerLookup = make(Lookup)
	webProviderLookup = make(Lookup)
	for _, p := range providerVals {
		s := providerValLookup[p]
		if p <= maxWebProvider {
			webProviderVals = append(webProviderVals, p)
			webProviders = append(webProviders, s)
			webValLookup[p] = s
			webProviderLookup[s] = p
			AllSupportedWebProvidersBitmap |= p
		}
		providers = append(providers, s)
		providerLookup[s] = p
	}
	AllSupportedWebProviders = strings.Join(webProviders, ", ")
	providerLookup[BrotliAltValue] = Brotli
	providerLookup[ZstandardAltValue] = Zstandard
}

func (p Provider) String() string {
	if v, ok := providerValLookup[p]; ok {
		return v
	}
	return strconv.Itoa(int(p))
}

// WebProviders returns the list of encodings that are known to be decodable by web browsers.
// This can be overlapped with the client's accepted encodings to determine which supported
// encodings can be applied to the ResponseWriter
func WebProviders() []string {
	return slices.Clone(webProviders)
}

// Providers returns the list of encodings that are known to be decodable in a web browser
func Providers() []string {
	return slices.Clone(providers)
}

// GetCompatibleWebProviders returns the string and the bitmap of the compatible providers
// negotiated between Trickster and the Client. The string representation is compatible with
// the Accept-Encoding header
func GetCompatibleWebProviders(acceptedEncodings string) (string, Provider) {
	var b Provider
	var s string
	// if an empty acceptedEncodings is provided, exit asap
	if acceptedEncodings == s {
		return s, b
	}
	// this converts the acceptedEncodings string into a bitmap of Trickster-compatible encoders
	for enc := range strings.SplitSeq(acceptedEncodings, ",") {
		if v, ok := webProviderLookup[strings.TrimSpace(enc)]; ok {
			b |= v
		}
	}
	// if there were no compatible encoders accepted, exit asap
	if b == 0 {
		return s, b
	}
	comp := make([]string, len(providerValLookup))
	var k int
	// otherwise, this builds the list of compatible encoders from the bitmap
	for i := Provider(1); i <= maxWebProvider; i <<= 1 {
		if b&i == i {
			comp[k] = providerValLookup[i]
			k++
		}
	}
	return strings.Join(comp[:k], ", "), b
}

// Clone returns a perfect copy of the lookup
func (l Lookup) Clone() Lookup {
	return maps.Clone(l)
}

// ProviderID returns the byte value of the provided encoding provider name
func ProviderID(providerName string) Provider {
	if b, ok := providerLookup[providerName]; ok {
		return b
	}
	return 0
}
