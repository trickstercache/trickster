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
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/brotli"
	"github.com/trickstercache/trickster/v2/pkg/encoding/deflate"
	"github.com/trickstercache/trickster/v2/pkg/encoding/gzip"
	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"
	"github.com/trickstercache/trickster/v2/pkg/encoding/snappy"
	"github.com/trickstercache/trickster/v2/pkg/encoding/zstd"
)

type EncoderInitializer func(io.Writer, int) io.WriteCloser
type DecoderInitializer func(io.Reader) reader.ReadCloserResetter

// GetEncoderInitializer returns an EncoderInitializer - one that can be passed an io.Writer
// to get an encoder wrapper for the passed writer, as well as an encoding value that is
// compatible with Content-Encoding headers
func GetEncoderInitializer(provider string) (EncoderInitializer, string) {
	p := ProviderID(provider)
	if p == 0 {
		return nil, ""
	}
	return SelectEncoderInitializer(p)
}

// SelectEncoderInitializer returns an EncoderInitializer based on the provided providers bitmap
func SelectEncoderInitializer(p Provider) (EncoderInitializer, string) {
	if p&Zstandard == Zstandard {
		return zstd.NewEncoder, ZstandardValue
	}
	if p&Brotli == Brotli {
		return brotli.NewEncoder, BrotliValue
	}
	if p&GZip == GZip {
		return gzip.NewEncoder, GZipValue
	}
	if p&Deflate == Deflate {
		return deflate.NewEncoder, DeflateValue
	}
	if p&Snappy == Snappy {
		return snappy.NewEncoder, ""
	}
	return nil, ""
}

// GetDecoderInitializer returns a DecoderInitializer - one that can be passed an io.ReadCloser
// to get a decoder Reader for the passed Reader
func GetDecoderInitializer(provider string) DecoderInitializer {
	p := ProviderID(provider)
	if p == 0 {
		return nil
	}
	return SelectDecoderInitializer(p)
}

// SelectEncoderInitializer returns an EncoderInitializer based on the provided providers bitmap
func SelectDecoderInitializer(p Provider) DecoderInitializer {
	if p&Zstandard == Zstandard {
		return zstd.NewDecoder
	}
	if p&Brotli == Brotli {
		return brotli.NewDecoder
	}
	if p&GZip == GZip {
		return gzip.NewDecoder
	}
	if p&Deflate == Deflate {
		return deflate.NewDecoder
	}
	if p&Snappy == Snappy {
		return snappy.NewDecoder
	}
	return nil
}
