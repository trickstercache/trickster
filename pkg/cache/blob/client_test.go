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

package blob_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/header"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"

	"github.com/stretchr/testify/require"
)

const testKey = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

var errFault = errors.New("fault")

func newClient() (*blob.Client, *blobtest.MemStore) {
	s := blobtest.NewMemStore()
	return blob.NewClient(s, "test", "filesystem"), s
}

func requireMiss(t *testing.T, c *blob.Client, key string) {
	t.Helper()
	b, s, err := c.Retrieve(key)
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, s)
	require.Nil(t, b)
}

func TestStoreRetrieveRemove(t *testing.T) {
	c, s := newClient()
	require.NoError(t, c.Connect())
	requireMiss(t, c, testKey)

	for _, key := range blobtest.Keys {
		require.NoError(t, c.Store(key, []byte("value of "+key), time.Minute))
	}
	for _, key := range blobtest.Keys {
		b, st, err := c.Retrieve(key)
		require.NoError(t, err)
		require.Equal(t, status.LookupStatusHit, st)
		require.Equal(t, "value of "+key, string(b))
	}

	require.NoError(t, c.Store(testKey, nil, time.Minute))
	b, st, err := c.Retrieve(testKey)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, st)
	require.Empty(t, b)

	require.NoError(t, c.Remove(testKey))
	requireMiss(t, c, testKey)
	require.Equal(t, len(blobtest.Keys)-1, s.Len())
	require.NoError(t, c.Close())
}

func TestStoreSplit(t *testing.T) {
	c, s := newClient()
	require.NoError(t, c.StoreSplit(testKey, []byte("meta"), []byte("body"), 0))
	b, _, err := c.Retrieve(testKey)
	require.NoError(t, err)
	require.Equal(t, "metabody", string(b))

	frame, _ := s.Frame(testKey)
	h, key, err := header.Parse(frame, int64(len(frame)))
	require.NoError(t, err)
	require.Equal(t, testKey, string(key))
	require.Equal(t, uint32(4), h.MetaLen)
	require.Equal(t, uint64(4), h.BodyLen)
	require.Zero(t, h.Expiration, "no ttl never expires")
	require.InDelta(t, time.Now().UnixNano(), h.LastWrite, float64(time.Minute))
}

func TestRetrieveSplit(t *testing.T) {
	c, s := newClient()
	require.True(t, c.SupportsSplit())
	require.NoError(t, c.StoreSplit(testKey, []byte("meta"), []byte("body"), time.Minute))
	meta, body, st, err := c.RetrieveSplit(testKey)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, st)
	require.Equal(t, "meta", string(meta))
	require.Equal(t, "body", string(body))
	// the sections share a buffer, and growing the one must not write over the other
	meta = append(meta, "more"...)
	require.Equal(t, "body", string(body))

	require.NoError(t, c.Store("whole", []byte("value"), time.Minute))
	meta, body, _, err = c.RetrieveSplit("whole")
	require.NoError(t, err)
	require.Empty(t, meta)
	require.Equal(t, "value", string(body))

	meta, body, st, err = c.RetrieveSplit("absent")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.Equal(t, status.LookupStatusKeyMiss, st)
	require.Nil(t, meta)
	require.Nil(t, body)
	s.OpenErr = errFault
	_, _, st, err = c.RetrieveSplit(testKey)
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)
}

func readBody(t *testing.T, b cache.Body) string {
	t.Helper()
	out, err := io.ReadAll(b)
	require.NoError(t, err)
	return string(out)
}

// one store keeps its objects open while they are read, the other does not
func stores() map[string]func() (*blob.Client, *blobtest.MemStore) {
	return map[string]func() (*blob.Client, *blobtest.MemStore){
		"open": func() (*blob.Client, *blobtest.MemStore) {
			c, s := newClient()
			s.CanStream = true
			return c, s
		},
		"brief": newClient,
	}
}

func TestOpenSplit(t *testing.T) {
	for name, newClient := range stores() {
		t.Run(name, func(t *testing.T) {
			c, s := newClient()
			require.True(t, c.SupportsStream())
			require.NoError(t, c.StoreSplit(testKey, []byte("meta"), []byte("0123456789"), time.Minute))
			meta, body, st, err := c.OpenSplit(testKey)
			require.NoError(t, err)
			require.Equal(t, status.LookupStatusHit, st)
			require.Equal(t, "meta", string(meta))
			require.Equal(t, int64(10), body.Size())
			part := make([]byte, 4)
			n, err := body.ReadAt(part, 3)
			require.NoError(t, err)
			require.Equal(t, "3456", string(part[:n]))
			n, err = body.ReadAt(part, 8)
			require.ErrorIs(t, err, io.EOF, "the body ends where the object does")
			require.Equal(t, "89", string(part[:n]))
			for _, off := range []int64{-1, 10, 11} {
				n, err = body.ReadAt(part, off)
				require.Error(t, err, off)
				require.Zero(t, n, off)
			}
			require.Equal(t, "0123456789", readBody(t, body))
			require.Empty(t, readBody(t, body), "a body is read through once")
			all, err := body.ReadAll()
			require.NoError(t, err)
			require.Equal(t, "0123456789", string(all))
			require.NoError(t, body.Close())

			// sections too long to be read along with the header
			long := strings.Repeat("m", 2*header.PrefixLen)
			require.NoError(t, c.StoreSplit("long", []byte(long), []byte("body"), 0))
			meta, body, _, err = c.OpenSplit("long")
			require.NoError(t, err)
			require.Equal(t, long, string(meta))
			require.Equal(t, "body", readBody(t, body))

			require.NoError(t, c.Store("whole", nil, 0))
			meta, body, _, err = c.OpenSplit("whole")
			require.NoError(t, err)
			require.Empty(t, meta)
			require.Zero(t, body.Size())
			require.Empty(t, readBody(t, body))
			all, err = body.ReadAll()
			require.NoError(t, err)
			require.Empty(t, all)

			// a body that is not the one the object was stored with
			frame, _ := s.Frame(testKey)
			frame = bytes.Clone(frame)
			frame[len(frame)-1] = 'X'
			s.SetFrame(testKey, frame)
			_, body, _, err = c.OpenSplit(testKey)
			require.NoError(t, err)
			require.Equal(t, "012345678X", readBody(t, body), "a body read in parts is not verified")
			_, err = body.ReadAll()
			require.ErrorIs(t, err, header.ErrCorrupt)
		})
	}
}

// a store's object is as it was when it was opened, for as long as it is open
func TestOpenBodyOutlastsItsObject(t *testing.T) {
	c, s := stores()["open"]()
	require.NoError(t, c.StoreSplit(testKey, nil, []byte("first"), time.Minute))
	_, body, _, err := c.OpenSplit(testKey)
	require.NoError(t, err)
	require.NoError(t, c.StoreSplit(testKey, nil, []byte("second"), time.Minute))
	require.NoError(t, s.Delete(testKey))
	require.Equal(t, "first", readBody(t, body))
}

// an object that is opened anew for each read must be, at each, the one that was opened first
func TestBriefBodyOfAChangedObject(t *testing.T) {
	for name, change := range map[string]func(c *blob.Client, s *blobtest.MemStore){
		"written again": func(c *blob.Client, _ *blobtest.MemStore) {
			time.Sleep(time.Millisecond)
			require.NoError(t, c.StoreSplit(testKey, []byte("meta"), []byte("first"), time.Minute))
		},
		"written anew": func(c *blob.Client, _ *blobtest.MemStore) {
			require.NoError(t, c.StoreSplit(testKey, []byte("meta"), []byte("other"), time.Minute))
		},
		"removed":     func(_ *blob.Client, s *blobtest.MemStore) { require.NoError(t, s.Delete(testKey)) },
		"not a frame": func(_ *blob.Client, s *blobtest.MemStore) { s.SetFrame(testKey, []byte("what is this")) },
		"cannot open": func(_ *blob.Client, s *blobtest.MemStore) { s.OpenErr = errFault },
		"cannot read": func(_ *blob.Client, s *blobtest.MemStore) { s.ReadErr = errFault },
		"body cut off": func(_ *blob.Client, s *blobtest.MemStore) {
			f, _ := s.Frame(testKey)
			s.SetFrame(testKey, f[:len(f)-1])
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, s := stores()["brief"]()
			require.NoError(t, c.StoreSplit(testKey, []byte("meta"), []byte("first"), time.Minute))
			_, body, _, err := c.OpenSplit(testKey)
			require.NoError(t, err)
			require.Equal(t, "first", readBody(t, body))
			_, body, _, err = c.OpenSplit(testKey)
			require.NoError(t, err)

			change(c, s)
			_, err = body.ReadAt(make([]byte, 1), 0)
			require.Error(t, err)
			if name != "cannot open" && name != "cannot read" {
				require.ErrorIs(t, err, blob.ErrObjectChanged)
			}
			_, err = io.ReadAll(body)
			require.Error(t, err)
			_, err = body.ReadAll()
			require.Error(t, err)
			require.NoError(t, body.Close())
		})
	}
}

type limitedWriter struct {
	bytes.Buffer
	limit int
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	if w.Len()+len(b) > w.limit {
		n, _ := w.Buffer.Write(b[:w.limit-w.Len()])
		return n, nil
	}
	return w.Buffer.Write(b)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errFault
}

func TestBriefBodyWriteTo(t *testing.T) {
	c, s := stores()["brief"]()
	want := bytes.Repeat([]byte("0123456789"), 60_000)
	require.NoError(t, c.StoreSplit(testKey, []byte("meta"), want, time.Minute))
	open := func() cache.Body {
		_, body, _, err := c.OpenSplit(testKey)
		require.NoError(t, err)
		return body
	}

	body := open()
	opens := s.Opens.Load()
	var out bytes.Buffer
	n, err := io.Copy(struct{ io.Writer }{&out}, body)
	require.NoError(t, err)
	require.EqualValues(t, len(want), n)
	require.Equal(t, want, out.Bytes())
	require.EqualValues(t, 3, s.Opens.Load()-opens, "600 KB streams in three parts")
	n, err = body.(io.WriterTo).WriteTo(&out)
	require.NoError(t, err)
	require.Zero(t, n, "a body is written through once")

	// a part already read is not read again
	body = open()
	_, err = io.ReadFull(body, make([]byte, 10))
	require.NoError(t, err)
	out.Reset()
	_, err = body.(io.WriterTo).WriteTo(&out)
	require.NoError(t, err)
	require.Equal(t, want[10:], out.Bytes())

	_, err = open().(io.WriterTo).WriteTo(failingWriter{})
	require.ErrorIs(t, err, errFault)
	_, err = open().(io.WriterTo).WriteTo(&limitedWriter{limit: 100})
	require.ErrorIs(t, err, io.ErrShortWrite)

	body = open()
	time.Sleep(time.Millisecond)
	require.NoError(t, c.StoreSplit(testKey, []byte("meta"), want, time.Minute))
	_, err = body.(io.WriterTo).WriteTo(&out)
	require.ErrorIs(t, err, blob.ErrObjectChanged)
}

func TestOpenSplitMisses(t *testing.T) {
	c, s := newClient()
	requireOpenMiss := func(key string) {
		t.Helper()
		meta, body, st, err := c.OpenSplit(key)
		require.ErrorIs(t, err, cache.ErrKNF)
		require.Equal(t, status.LookupStatusKeyMiss, st)
		require.Nil(t, meta)
		require.Nil(t, body)
	}
	requireOpenMiss("absent")

	require.NoError(t, c.Store("other", []byte("value"), time.Minute))
	frame, _ := s.Frame("other")
	s.SetFrame(testKey, frame)
	requireOpenMiss(testKey)
	require.Equal(t, 2, s.Len(), "another key's object is left for its own key")

	require.NoError(t, c.Store(testKey, []byte("value"), time.Nanosecond))
	time.Sleep(time.Millisecond)
	requireOpenMiss(testKey)
	s.SetFrame(testKey, frame[:len(frame)-1])
	requireOpenMiss(testKey)
	s.SetFrame(testKey, []byte("not a frame"))
	requireOpenMiss(testKey)
	require.Equal(t, 1, s.Len(), "what cannot be served is removed")
}

func TestOpenSplitErrors(t *testing.T) {
	c, s := newClient()
	s.CanStream = true
	require.NoError(t, c.StoreSplit(testKey, []byte(strings.Repeat("m", 2*header.PrefixLen)), nil, 0))
	s.OpenErr = errFault
	_, _, st, err := c.OpenSplit(testKey)
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)

	s.OpenErr, s.ReadErr = nil, errFault
	_, _, st, err = c.OpenSplit(testKey)
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)

	// the header is read, and the meta section after it is not
	s.ReadErr = nil
	s.ReadErrAfter = 1
	_, _, st, err = c.OpenSplit(testKey)
	require.ErrorIs(t, err, blobtest.ErrReadAfter)
	require.Equal(t, status.LookupStatusError, st)
	require.Equal(t, 1, s.Len(), "a failed read removes nothing")

	// the object is opened, and its body is not read
	require.NoError(t, c.StoreSplit("short", []byte("meta"), []byte("body"), 0))
	_, body, _, err := c.OpenSplit("short")
	require.NoError(t, err)
	_, err = body.ReadAll()
	require.ErrorIs(t, err, blobtest.ErrReadAfter)
}

func TestStoreErrors(t *testing.T) {
	c, s := newClient()
	require.ErrorIs(t, c.Store("", []byte("v"), time.Minute), blob.ErrKeyRequired)
	require.ErrorIs(t, c.Store(strings.Repeat("k", header.MaxKeyLen+1), nil, 0), header.ErrKeyTooLong)
	require.Zero(t, s.Len())

	// a key longer than the pooled header buffer still stores, and the pool is not grown by it
	long := strings.Repeat("k", header.MaxKeyLen)
	require.NoError(t, c.Store(long, []byte("v"), 0))
	b, _, err := c.Retrieve(long)
	require.NoError(t, err)
	require.Equal(t, "v", string(b))

	s.PutErr = errFault
	require.ErrorIs(t, c.Store(testKey, []byte("v"), time.Minute), errFault)
}

func TestRetrieveErrors(t *testing.T) {
	c, s := newClient()
	require.NoError(t, c.Store(testKey, []byte("v"), time.Minute))

	s.ReadErr = errFault
	_, st, err := c.Retrieve(testKey)
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)

	s.ReadErr, s.OpenErr = nil, errFault
	_, st, err = c.Retrieve(testKey)
	require.ErrorIs(t, err, errFault)
	require.Equal(t, status.LookupStatusError, st)

	s.OpenErr = nil
	require.Equal(t, 1, s.Len(), "a failed read removes nothing")
}

func TestRetrieveExpired(t *testing.T) {
	c, s := newClient()
	require.NoError(t, c.Store(testKey, []byte("v"), time.Nanosecond))
	time.Sleep(time.Millisecond)
	requireMiss(t, c, testKey)
	require.Zero(t, s.Len(), "an expired object is removed")
}

func TestRetrieveInvalid(t *testing.T) {
	c, s := newClient()
	require.NoError(t, c.Store(testKey, []byte("value"), time.Minute))
	good, _ := s.Frame(testKey)

	tests := []struct {
		name  string
		frame []byte
	}{
		{"truncated", good[:len(good)-1]},
		{"payload changed", append(bytes.Clone(good[:len(good)-1]), 'X')},
		{"header changed", append([]byte{good[0], good[1], good[2], good[3], good[4], 9}, good[6:]...)},
		{"another format", []byte("\x86\xa3key\xa1k an object the index once wrapped")},
		{"empty", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s.SetFrame(testKey, test.frame)
			requireMiss(t, c, testKey)
			require.Zero(t, s.Len(), "an invalid object is removed")
		})
	}
}

// a large object is returned before its checksum is verified, and removed after if it fails
func TestRetrieveVerifiesLargeObjectsLater(t *testing.T) {
	c, s := newClient()
	body := bytes.Repeat([]byte("large"), blob.DeferredVerifyLen/5+1)
	require.NoError(t, c.Store(testKey, body, time.Minute))
	b, st, err := c.Retrieve(testKey)
	require.NoError(t, err)
	require.Equal(t, status.LookupStatusHit, st)
	require.True(t, bytes.Equal(body, b))
	require.NoError(t, c.Close())
	require.Equal(t, 1, s.Len(), "a whole object stays")

	frame, _ := s.Frame(testKey)
	frame = bytes.Clone(frame)
	frame[len(frame)-1] ^= 1
	s.SetFrame(testKey, frame)
	b, st, err = c.Retrieve(testKey)
	require.NoError(t, err, "served once")
	require.Equal(t, status.LookupStatusHit, st)
	require.Len(t, b, len(body))
	require.NoError(t, c.Close(), "waits for the check")
	require.Zero(t, s.Len(), "and the damaged object is gone")
	requireMiss(t, c, testKey)
}

func TestRetrieveAnotherKeysObject(t *testing.T) {
	c, s := newClient()
	require.NoError(t, c.Store("other", []byte("value"), time.Minute))
	frame, _ := s.Frame("other")
	s.SetFrame(testKey, frame)
	requireMiss(t, c, testKey)
	require.Equal(t, 2, s.Len(), "a whole object is left for its own key")
}

func TestScanMeta(t *testing.T) {
	c, s := newClient()
	start := time.Now()
	require.NoError(t, c.Store("a-live", []byte("12345"), time.Hour))
	require.NoError(t, c.StoreSplit("b-forever", []byte("12"), []byte("345678"), 0))
	require.NoError(t, c.Store("c-expired", []byte("1"), time.Nanosecond))
	s.SetFrame("d-corrupt", []byte("not a frame"))
	good, _ := s.Frame("a-live")
	s.SetFrame("e-torn", good[:len(good)-2])
	time.Sleep(time.Millisecond)

	var got []cache.ObjectMeta
	next, done, err := c.ScanMeta("", 100, func(m cache.ObjectMeta) { got = append(got, m) })
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "e-torn", next)
	require.Len(t, got, 2)

	require.Equal(t, "a-live", got[0].Key)
	require.Equal(t, int64(5), got[0].Size)
	require.WithinDuration(t, start.Add(time.Hour), got[0].Expiration, time.Minute)
	require.WithinDuration(t, start, got[0].LastWrite, time.Minute)
	require.Equal(t, "b-forever", got[1].Key)
	require.Equal(t, int64(8), got[1].Size)
	require.True(t, got[1].Expiration.IsZero())

	require.Equal(t, 2, s.Len(), "what cannot be served is removed")

	next, done, err = c.ScanMeta("", 1, func(cache.ObjectMeta) {})
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, "a-live", next)

	s.ScanErr = errFault
	_, _, err = c.ScanMeta("", 1, func(cache.ObjectMeta) {})
	require.ErrorIs(t, err, errFault)
}

func TestCapabilities(t *testing.T) {
	c, s := newClient()
	ms, ok := c.MetaStore()
	require.True(t, ok)
	require.Same(t, s, ms)

	s.Free = 99
	free, ok := c.FreeBytes()
	require.True(t, ok)
	require.Equal(t, int64(99), free)
	s.FreeErr = errFault
	_, ok = c.FreeBytes()
	require.False(t, ok)

	bare := blob.NewClient(plainStore{s}, "test", "filesystem")
	_, ok = bare.MetaStore()
	require.False(t, ok)
	_, ok = bare.FreeBytes()
	require.False(t, ok)
}

// hides the optional capabilities of the Store it wraps
type plainStore struct{ blob.Store }

func BenchmarkClient(b *testing.B) {
	for _, size := range []int{1 << 10, 64 << 10, 4 << 20} {
		c, _ := newClient()
		data := make([]byte, size)
		b.Run("store/"+time.Duration(size).String(), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				c.Store(testKey, data, time.Hour)
			}
		})
		b.Run("retrieve/"+time.Duration(size).String(), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for b.Loop() {
				c.Retrieve(testKey)
			}
		})
	}
}

// an object stored anew while a damaged one is being removed is not removed with it
func TestDiscardSparesAReplacement(t *testing.T) {
	c, s := newClient()
	replace := func() { require.NoError(t, c.Store(testKey, []byte("replacement"), time.Minute)) }
	for name, damage := range map[string]func(frame []byte) []byte{
		"payload":     func(f []byte) []byte { f[len(f)-1] ^= 1; return f },
		"header":      func(f []byte) []byte { f[5] ^= 1; return f },
		"truncated":   func(f []byte) []byte { return f[:len(f)-1] },
		"not a frame": func([]byte) []byte { return []byte("what is this") },
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, c.Store(testKey, []byte("damaged"), time.Minute))
			frame, _ := s.Frame(testKey)
			s.SetFrame(testKey, damage(bytes.Clone(frame)))
			s.OnDeleteIf = replace
			requireMiss(t, c, testKey)
			s.OnDeleteIf = nil
			b, _, err := c.Retrieve(testKey)
			require.NoError(t, err)
			require.Equal(t, "replacement", string(b))
		})
	}
	t.Run("expired", func(t *testing.T) {
		require.NoError(t, c.Store(testKey, []byte("expired"), time.Nanosecond))
		time.Sleep(time.Millisecond)
		s.OnDeleteIf = replace
		requireMiss(t, c, testKey)
		s.OnDeleteIf = nil
		b, _, err := c.Retrieve(testKey)
		require.NoError(t, err)
		require.Equal(t, "replacement", string(b))
	})
	t.Run("verified later", func(t *testing.T) {
		body := bytes.Repeat([]byte("large"), blob.DeferredVerifyLen/5+1)
		require.NoError(t, c.Store(testKey, body, time.Minute))
		frame, _ := s.Frame(testKey)
		frame = bytes.Clone(frame)
		frame[len(frame)-1] ^= 1
		s.SetFrame(testKey, frame)
		s.OnDeleteIf = replace
		_, _, err := c.Retrieve(testKey)
		require.NoError(t, err)
		require.NoError(t, c.Close())
		s.OnDeleteIf = nil
		b, _, err := c.Retrieve(testKey)
		require.NoError(t, err)
		require.Equal(t, "replacement", string(b))
	})
}
