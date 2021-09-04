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

package profile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/encoding/providers"

	strutil "github.com/trickstercache/trickster/v2/pkg/util/strings"
)

type Profile struct {
	// ClientAcceptEncoding holds the Client's Accept-Encoding header value, as received
	ClientAcceptEncoding string
	// Supported is the Client-Accepted Encodings filtered against Trickster supported Encodings
	// represented as a bitmap
	Supported providers.Provider
	// SupportedHeaderVal is the Accept-Encoding value representation of the Supported byte
	// that is used when proxying to an origin
	SupportedHeaderVal string
	// NoTransform is true if the client sent a Cache-Control header with a 'no-transform' value
	NoTransform bool
	// ContentEncoding holds the Response's Content-Encoding header value, if any
	ContentEncoding string
	// ContentType holds the Response's Content-Type header value, if any
	ContentType string
	// CompressTypes is a lookup map of any Content Types that should be compressed
	CompressTypes strutil.Lookup
	// ContentEncodingNum is the numeric value for the ContentEncoding string
	ContentEncodingNum providers.Provider

	Level int // todo: set this at some point
}

func (p *Profile) Clone() *Profile {
	return &Profile{
		ClientAcceptEncoding: p.ClientAcceptEncoding,
		Supported:            p.Supported,
		SupportedHeaderVal:   p.SupportedHeaderVal,
		NoTransform:          p.NoTransform,
		ContentEncoding:      p.ContentEncoding,
		CompressTypes:        p.CompressTypes,
		ContentType:          p.ContentType,
		Level:                p.Level,
	}
}

func (p *Profile) String() string {
	lines := make([]string, 0, 7)
	if p.ClientAcceptEncoding != "" {
		lines = append(lines, fmt.Sprintf(`"clientAcceptEncoding":"%s"`, p.ClientAcceptEncoding))
	}
	if p.SupportedHeaderVal != "" {
		lines = append(lines, fmt.Sprintf(`"supportedHeaderVal":"%s"`, p.SupportedHeaderVal))
	}
	if p.Supported > 0 {
		lines = append(lines, fmt.Sprintf(`"supportedBitmap":%d`, p.Supported))
	}
	if p.NoTransform {
		lines = append(lines, fmt.Sprintf(`"noTransform":%t`, p.NoTransform))
	}
	if len(p.CompressTypes) > 0 {
		vals := make([]string, len(p.CompressTypes))
		var i int
		for k := range p.CompressTypes {
			vals[i] = `"` + k + `"`
			i++
		}
		sort.Strings(vals)
		lines = append(lines, fmt.Sprintf(`"compressTypes":[%s]`, strings.Join(vals, ",")))
	}
	if p.ContentEncoding != "" {
		lines = append(lines, fmt.Sprintf(`"contentEncoding":"%s"`, p.ContentEncoding))
	}
	if p.ContentType != "" {
		lines = append(lines, fmt.Sprintf(`"contentType":"%s"`, p.ContentType))
	}
	if p.Level > -1 {
		lines = append(lines, fmt.Sprintf(`"level":"%d"`, p.Level))
	}
	return "{" + strings.Join(lines, ",") + "}"
}

func (p *Profile) ClientAcceptsEncoding(enc providers.Provider) bool {
	return p.Supported&enc == enc
}

// GetEncoderInitializer returns an encoder based on the profile's Content-Type and
// Accept-Encoding headers
func (p *Profile) GetEncoderInitializer() (providers.EncoderInitializer, string) {
	// do not setup an encoder if the request won't accept any encodings supported by trickster
	if p.Supported == 0 {
		return nil, ""
	}
	if p.ContentType == "" {
		return nil, ""
	}

	// this extracts the first part of a content type like 'text/plain; charset=utf-8',
	// as 'text/plain', since that is what is matched for in the compressible types list
	ct := p.ContentType
	if i := strings.Index(ct, ";"); i > -1 {
		ct = ct[:i]
	}

	// do not setup an encoder if the response's Content-Type is not configured as compressible
	if _, ok := p.CompressTypes[ct]; !ok {
		return nil, ""
	}

	return providers.SelectEncoderInitializer(p.Supported)
}

// GetDecoderInitializer returns a decoder based on the profile's Content-Encoding header
func (p *Profile) GetDecoderInitializer() providers.DecoderInitializer {
	return providers.SelectDecoderInitializer(p.ContentEncodingNum)
}
