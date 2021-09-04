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

package snappy

import (
	"io"

	"github.com/trickstercache/trickster/v2/pkg/encoding/reader"

	"github.com/golang/snappy"
)

// Decode returns the decoded version of the encoded byte slice
func Decode(in []byte) ([]byte, error) {
	return snappy.Decode(nil, in)
}

// Encode returns the encoded version of the byte slice
func Encode(in []byte) ([]byte, error) {
	b := snappy.Encode(nil, in)
	return b, nil
}

func NewEncoder(w io.Writer, unused int) io.WriteCloser {
	return snappy.NewWriter(w)
}

func NewDecoder(r io.Reader) reader.ReadCloserResetter {
	return reader.NewReadCloserResetter(snappy.NewReader(r))
}
