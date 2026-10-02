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

package header

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testFrame(t testing.TB, key string, meta, body []byte) ([]byte, Header) {
	t.Helper()
	h := Header{
		Flags: 1, Expiration: 1_900_000_000_000_000_000, LastWrite: 1_800_000_000_000_000_000,
		MetaLen: uint32(len(meta)), BodyLen: uint64(len(body)), PayloadCRC: Checksum(meta, body),
	}
	b, err := Append(nil, key, &h)
	require.NoError(t, err)
	h.KeyLen = uint16(len(key))
	return append(append(b, meta...), body...), h
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name, key  string
		meta, body []byte
	}{
		{"body only", testKey, nil, []byte("body")},
		{"meta and body", testKey, []byte("meta"), []byte("body")},
		{"empty payload", "k", nil, nil},
		{"empty key", "", nil, []byte("x")},
		{"longest key", strings.Repeat("k", MaxKeyLen), []byte("m"), []byte("b")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frame, want := testFrame(t, test.key, test.meta, test.body)
			got, key, err := Parse(frame, int64(len(frame)))
			require.NoError(t, err)
			require.Equal(t, want, got)
			require.Equal(t, test.key, string(key))
			require.Equal(t, int64(len(frame)), got.FrameSize())
			require.Equal(t, int64(Size+len(test.key)+len(test.meta)), got.BodyOffset())
			payload := frame[got.PayloadOffset():]
			require.NoError(t, VerifyPayload(&got, payload))
			require.True(t, bytes.Equal(test.body, frame[got.BodyOffset():]))
		})
	}
}

func TestAppendKeepsPrefixAndRejectsLongKey(t *testing.T) {
	h := Header{}
	b, err := Append([]byte("prefix"), "key", &h)
	require.NoError(t, err)
	require.Equal(t, "prefix", string(b[:6]))
	_, _, err = Parse(b[6:], int64(len(b)-6))
	require.NoError(t, err)

	b, err = Append([]byte("prefix"), strings.Repeat("k", MaxKeyLen+1), &h)
	require.ErrorIs(t, err, ErrKeyTooLong)
	require.Equal(t, "prefix", string(b))
}

func TestParseRejects(t *testing.T) {
	frame, _ := testFrame(t, testKey, []byte("meta"), []byte("body"))
	size := int64(len(frame))
	mutate := func(fn func(b []byte)) []byte {
		b := bytes.Clone(frame)
		fn(b)
		return b
	}
	tests := []struct {
		name  string
		frame []byte
		size  int64
		want  error
	}{
		{"short", frame[:Size-1], size, ErrBadMagic},
		{"magic", mutate(func(b []byte) { b[0] = 'X' }), size, ErrBadMagic},
		{"version", mutate(func(b []byte) { b[offVersion] = Version + 1 }), size, ErrBadMagic},
		{"flags", mutate(func(b []byte) { b[offFlags] ^= 1 }), size, ErrCorrupt},
		{"expiration", mutate(func(b []byte) { b[offExpiration] ^= 1 }), size, ErrCorrupt},
		{"key byte", mutate(func(b []byte) { b[Size] ^= 1 }), size, ErrCorrupt},
		{"header checksum", mutate(func(b []byte) { b[offHeaderCRC] ^= 1 }), size, ErrCorrupt},
		{"key length", mutate(func(b []byte) { b[offKeyLen+1] = 0xff }), size, ErrCorrupt},
		{"body length", mutate(func(b []byte) { b[offBodyLen+7] = 0xff }), size, ErrCorrupt},
		{"truncated frame", frame, size - 1, ErrCorrupt},
		{"oversized frame", frame, size + 1, ErrCorrupt},
		{"key cut off", frame[:Size+4], size, ErrCorrupt},
		{"size under header", frame, Size, ErrCorrupt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, key, err := Parse(test.frame, test.size)
			require.ErrorIs(t, err, test.want)
			require.Nil(t, key)
		})
	}
}

func TestPeek(t *testing.T) {
	frame, want := testFrame(t, testKey, []byte("meta"), []byte("body"))
	got, ok := Peek(frame[:Size])
	require.True(t, ok)
	require.Equal(t, want, got)
	_, ok = Peek(frame[:Size-1])
	require.False(t, ok)
	frame[0] = 'X'
	_, ok = Peek(frame)
	require.False(t, ok)
}

func TestVerifyPayload(t *testing.T) {
	frame, h := testFrame(t, testKey, []byte("meta"), []byte("body"))
	payload := frame[h.PayloadOffset():]
	require.NoError(t, VerifyPayload(&h, payload))
	require.ErrorIs(t, VerifyPayload(&h, payload[1:]), ErrCorrupt)
	flipped := bytes.Clone(payload)
	flipped[0] ^= 1
	require.ErrorIs(t, VerifyPayload(&h, flipped), ErrCorrupt)
}

func TestExpired(t *testing.T) {
	require.False(t, (&Header{}).Expired(1<<62), "zero never expires")
	require.False(t, (&Header{Expiration: 10}).Expired(9))
	require.True(t, (&Header{Expiration: 10}).Expired(10))
	require.True(t, (&Header{Expiration: 10}).Expired(11))
}

type failingReaderAt struct{ err error }

func (f failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, f.err }

func TestRead(t *testing.T) {
	frame, want := testFrame(t, testKey, nil, bytes.Repeat([]byte("b"), PrefixLen))
	buf := make([]byte, PrefixLen)

	got, key, err := Read(bytes.NewReader(frame), int64(len(frame)), buf)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, testKey, string(key))

	small, want := testFrame(t, "k", nil, []byte("b"))
	got, key, err = Read(bytes.NewReader(small), int64(len(small)), buf)
	require.NoError(t, err, "a frame shorter than the scratch buffer")
	require.Equal(t, want, got)
	require.Equal(t, "k", string(key))

	_, _, err = Read(bytes.NewReader(small[:10]), int64(len(small)), buf)
	require.ErrorIs(t, err, io.EOF, "a frame shorter than its reported size")

	errRead := errors.New("read failed")
	_, _, err = Read(failingReaderAt{errRead}, int64(len(small)), buf)
	require.ErrorIs(t, err, errRead)
}

func FuzzParse(f *testing.F) {
	frame, _ := testFrame(f, testKey, []byte("meta"), []byte("body"))
	f.Add(frame, int64(len(frame)))
	f.Add(frame[:Size], int64(len(frame)))
	f.Add([]byte{}, int64(0))
	f.Fuzz(func(t *testing.T, b []byte, size int64) {
		h, key, err := Parse(b, size)
		if err != nil {
			return
		}
		if len(key) != int(h.KeyLen) || h.FrameSize() != size || h.PayloadLen() < 0 {
			t.Fatalf("accepted an inconsistent header: %+v", h)
		}
		out, err := Append(nil, string(key), &h)
		if err != nil || !bytes.Equal(out, b[:len(out)]) {
			t.Fatalf("accepted header does not re-encode to its input: %v", err)
		}
	})
}

func BenchmarkAppend(b *testing.B) {
	h := Header{Expiration: 1, LastWrite: 2, BodyLen: 3}
	buf := make([]byte, 0, Size+len(testKey))
	b.ReportAllocs()
	for b.Loop() {
		Append(buf, testKey, &h)
	}
}

func BenchmarkParse(b *testing.B) {
	frame, _ := testFrame(b, testKey, nil, []byte("body"))
	b.ReportAllocs()
	for b.Loop() {
		Parse(frame, int64(len(frame)))
	}
}

func BenchmarkChecksum(b *testing.B) {
	for _, size := range []int{1 << 10, 4 << 20} {
		body := make([]byte, size)
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				Checksum(nil, body)
			}
		})
	}
}
