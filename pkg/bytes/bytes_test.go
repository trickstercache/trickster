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

package bytes

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The whole-body mode exists so that an oversized document fails instead of
// parsing as a plausible fragment. Every case below is about that boundary.
func TestReadBoundedBodyWholeBody(t *testing.T) {
	t.Run("under the limit", func(t *testing.T) {
		b, err := ReadBoundedBody(strings.NewReader("hello"), 10, false)
		require.NoError(t, err)
		require.Equal(t, "hello", string(b))
	})
	t.Run("exactly at the limit is allowed", func(t *testing.T) {
		b, err := ReadBoundedBody(strings.NewReader("hello"), 5, false)
		require.NoError(t, err)
		require.Equal(t, "hello", string(b))
	})
	t.Run("one byte over fails", func(t *testing.T) {
		_, err := ReadBoundedBody(strings.NewReader("hello!"), 5, false)
		require.ErrorIs(t, err, ErrBodyTooLarge)
		require.Contains(t, err.Error(), "limit 5")
	})
	t.Run("oversized returns no partial body", func(t *testing.T) {
		b, err := ReadBoundedBody(strings.NewReader(strings.Repeat("x", 100)), 5, false)
		require.Error(t, err)
		require.Nil(t, b,
			"a truncated body must not be returned; callers would parse it")
	})
	t.Run("empty body", func(t *testing.T) {
		b, err := ReadBoundedBody(strings.NewReader(""), 5, false)
		require.NoError(t, err)
		require.Empty(t, b)
	})
	t.Run("a zero limit admits only an empty body", func(t *testing.T) {
		b, err := ReadBoundedBody(strings.NewReader(""), 0, false)
		require.NoError(t, err)
		require.Empty(t, b)
		_, err = ReadBoundedBody(strings.NewReader("x"), 0, false)
		require.ErrorIs(t, err, ErrBodyTooLarge)
	})
	t.Run("a read error propagates", func(t *testing.T) {
		_, err := ReadBoundedBody(&failingReader{}, 100, false)
		require.ErrorIs(t, err, errRead)
	})
}

// The fixed-width mode reads a framed value and leaves the rest of the
// stream for whatever comes next.
func TestReadBoundedBodyTruncate(t *testing.T) {
	t.Run("reads exactly n and leaves the remainder", func(t *testing.T) {
		r := strings.NewReader("abcdef")
		b, err := ReadBoundedBody(r, 3, true)
		require.NoError(t, err)
		require.Equal(t, "abc", string(b))

		rest, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Equal(t, "def", string(rest),
			"a fixed-width read must not consume past its field")
	})
	t.Run("more available is not an error", func(t *testing.T) {
		_, err := ReadBoundedBody(strings.NewReader(strings.Repeat("x", 1000)), 4, true)
		require.NoError(t, err)
	})
	t.Run("a short read is an error", func(t *testing.T) {
		b, err := ReadBoundedBody(strings.NewReader("ab"), 4, true)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		require.Len(t, b, 4,
			"the partially-filled buffer is returned alongside the error")
	})
	t.Run("an empty reader is EOF", func(t *testing.T) {
		_, err := ReadBoundedBody(strings.NewReader(""), 4, true)
		require.ErrorIs(t, err, io.EOF)
	})
	t.Run("zero length reads nothing", func(t *testing.T) {
		r := strings.NewReader("abc")
		b, err := ReadBoundedBody(r, 0, true)
		require.NoError(t, err)
		require.Empty(t, b)
		rest, _ := io.ReadAll(r)
		require.Equal(t, "abc", string(rest))
	})
}

func TestReadBoundedBodyRejectsNegativeLimit(t *testing.T) {
	for _, truncate := range []bool{true, false} {
		_, err := ReadBoundedBody(strings.NewReader("x"), -1, truncate)
		require.ErrorIs(t, err, ErrNegativeLimit)
	}
}

var errRead = errors.New("read failed")

type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, errRead }
