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

package client

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFqdn(t *testing.T) {
	require.Equal(t, ".", Fqdn(""))
	require.Equal(t, "example.com.", Fqdn("example.com"))
	require.Equal(t, "example.com.", Fqdn("example.com."))
}

func TestPackName(t *testing.T) {
	b, err := packName(nil, "example.com.")
	require.NoError(t, err)
	require.Equal(t, []byte("\x07example\x03com\x00"), b)

	b, err = packName(nil, ".")
	require.NoError(t, err)
	require.Equal(t, []byte{0}, b)

	b, err = packName(nil, "")
	require.NoError(t, err)
	require.Equal(t, []byte{0}, b)
}

func TestPackNameErrors(t *testing.T) {
	_, err := packName(nil, "a..b")
	require.ErrorIs(t, err, ErrInvalidName, "empty labels are not encodable")

	_, err = packName(nil, strings.Repeat("a", maxLabelLen+1)+".com")
	require.ErrorIs(t, err, ErrInvalidName, "labels cap at 63 bytes")

	long := strings.TrimSuffix(strings.Repeat(strings.Repeat("a", 63)+".", 5), ".")
	_, err = packName(nil, long)
	require.ErrorIs(t, err, ErrInvalidName, "names cap at 255 bytes")
}

func TestUnpackName(t *testing.T) {
	msg := []byte("\x03com\x00\x03foo\xc0\x00")
	name, next, err := unpackName(msg, 5)
	require.NoError(t, err)
	require.Equal(t, "foo.com.", name)
	require.Equal(t, 11, next, "the offset resumes after the pointer, not the target")

	name, next, err = unpackName(msg, 0)
	require.NoError(t, err)
	require.Equal(t, "com.", name)
	require.Equal(t, 5, next)

	name, next, err = unpackName([]byte{0}, 0)
	require.NoError(t, err)
	require.Equal(t, ".", name)
	require.Equal(t, 1, next)
}

func TestUnpackNameEscapes(t *testing.T) {
	name, _, err := unpackName([]byte{4, 'a', '.', 'b', 0x01, 0}, 0)
	require.NoError(t, err)
	require.Equal(t, `a\.b\001.`, name,
		"separators and unprintable bytes are escaped in presentation form")
}

func TestUnpackNameErrors(t *testing.T) {
	tests := map[string]struct {
		msg  []byte
		off  int
		want error
	}{
		"self-referential pointer": {[]byte{0xc0, 0x00}, 0, ErrNameLoop},
		"reserved length prefix":   {[]byte{0x40}, 0, ErrInvalidName},
		"label runs past the end":  {[]byte{0x03, 'a'}, 0, ErrShortMessage},
		"pointer past the end":     {[]byte{0xc0, 0xff}, 0, ErrShortMessage},
		"pointer is a lone byte":   {[]byte{0xc0}, 0, ErrShortMessage},
		"offset past the end":      {[]byte{0}, 9, ErrShortMessage},
		"unterminated name":        {[]byte{0x01, 'a'}, 0, ErrShortMessage},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := unpackName(test.msg, test.off)
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestUnpackNameTooLong(t *testing.T) {
	// 5 maximum-length labels overrun the 255-byte name ceiling
	var msg []byte
	for range 5 {
		msg = append(msg, maxLabelLen)
		msg = append(msg, strings.Repeat("a", maxLabelLen)...)
	}
	msg = append(msg, 0)
	_, _, err := unpackName(msg, 0)
	require.ErrorIs(t, err, ErrInvalidName)
}
