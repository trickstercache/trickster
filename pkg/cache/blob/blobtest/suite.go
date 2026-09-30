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

package blobtest

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"

	"github.com/stretchr/testify/require"
)

const (
	hexKey      = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	scanObjects = 57
	scanBatch   = 10
	writers     = 8
	writes      = 50
)

// Keys are cache keys of every shape Trickster derives, which a Store must hold apart
var Keys = []string{
	hexKey,
	hexKey[:32],
	hexKey + ".1700000000000-1700003600000",
	strings.ToUpper(hexKey),
	"backend.prefix.pgwire.delta.d41d8cd98f00b204e9800998ecf8427e",
	"backend.prefix.pgwire.delta.d41d8cd98f00b204e9800998ecf8427e.fallback",
	"prefix|tenant:dpc:d41d8cd98f00b204e9800998ecf8427e:fallback",
	"prefix|" + strings.Repeat("t", 1024) + ":dpc:d41d8cd98f00b204e9800998ecf8427e",
	"../../escape",
	"cache.index",
	"k",
}

func readAll(t testing.TB, b blob.Blob) []byte {
	t.Helper()
	out := make([]byte, b.Size())
	n, err := b.ReadAt(out, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	require.Equal(t, len(out), n)
	return out
}

func get(t testing.TB, s blob.Store, key string) []byte {
	t.Helper()
	b, err := s.Open(key)
	require.NoError(t, err)
	defer b.Close()
	return readAll(t, b)
}

// leaves nothing open, so that the store can be closed
func requireStored(t testing.TB, s blob.Store, key string, want bool) {
	t.Helper()
	b, err := s.Open(key)
	if !want {
		require.ErrorIs(t, err, cache.ErrKNF, key)
		return
	}
	require.NoError(t, err, key)
	require.NoError(t, b.Close())
}

// RunStoreSuite checks the blob.Store that newStore returns, connected and empty,
// against the behavior every blob.Store promises
func RunStoreSuite(t *testing.T, newStore func(t *testing.T) blob.Store) {
	t.Run("put and open", func(t *testing.T) {
		s := newStore(t)
		for i, key := range Keys {
			require.NoError(t, s.Put(key, []byte("h"+strconv.Itoa(i)), []byte("meta"), []byte("body")))
		}
		for i, key := range Keys {
			require.Equal(t, "h"+strconv.Itoa(i)+"metabody", string(get(t, s, key)), key)
		}
	})

	t.Run("empty sections", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Put(hexKey, []byte("hdr"), nil, nil))
		require.Equal(t, "hdr", string(get(t, s, hexKey)))
		require.NoError(t, s.Put(hexKey, nil, nil, []byte("body")))
		require.Equal(t, "body", string(get(t, s, hexKey)))
	})

	t.Run("read at", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Put(hexKey, []byte("0123"), []byte("4567"), []byte("89")))
		b, err := s.Open(hexKey)
		require.NoError(t, err)
		defer b.Close()
		require.Equal(t, int64(10), b.Size())
		p := make([]byte, 4)
		n, err := b.ReadAt(p, 3)
		require.NoError(t, err)
		require.Equal(t, "3456", string(p[:n]))
		n, err = b.ReadAt(p, 8)
		require.ErrorIs(t, err, io.EOF)
		require.Equal(t, "89", string(p[:n]))
	})

	t.Run("overwrite", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Put(hexKey, []byte("a long first frame"), nil, nil))
		require.NoError(t, s.Put(hexKey, []byte("short"), nil, nil))
		require.Equal(t, "short", string(get(t, s, hexKey)))
	})

	t.Run("missing", func(t *testing.T) {
		s := newStore(t)
		requireStored(t, s, hexKey, false)
		require.NoError(t, s.Delete(hexKey, "never stored"))
		require.NoError(t, s.Delete())
	})

	t.Run("delete", func(t *testing.T) {
		s := newStore(t)
		for _, key := range Keys {
			require.NoError(t, s.Put(key, []byte("h"), nil, nil))
		}
		require.NoError(t, s.Delete(Keys[0], "never stored", Keys[1]))
		for i, key := range Keys {
			requireStored(t, s, key, i >= 2)
		}
	})

	t.Run("scan", func(t *testing.T) {
		s := newStore(t)
		_, done, err := s.Scan("", scanBatch, func(blob.Blob) bool { return false })
		require.NoError(t, err)
		require.True(t, done, "an empty store")

		want := make(map[string]bool, scanObjects)
		for i := range scanObjects {
			frame := "frame-" + strconv.Itoa(i)
			want[frame] = true
			require.NoError(t, s.Put("key-"+strconv.Itoa(i), []byte(frame), nil, nil))
		}
		seen := make(map[string]int, scanObjects)
		var after string
		var batches int
		for done = false; !done; batches++ {
			require.Less(t, batches, scanObjects, "scan does not end")
			var n int
			after, done, err = s.Scan(after, scanBatch, func(b blob.Blob) bool {
				n++
				frame := string(readAll(t, b))
				seen[frame]++
				// every third frame is removed by the scan itself
				return strings.HasSuffix(frame, "3") || strings.HasSuffix(frame, "6")
			})
			require.NoError(t, err)
			require.LessOrEqual(t, n, scanBatch)
		}
		require.Len(t, seen, scanObjects)
		for frame, n := range seen {
			require.True(t, want[frame], frame)
			require.Equal(t, 1, n, frame)
		}
		for i := range scanObjects {
			requireStored(t, s, "key-"+strconv.Itoa(i), i%10 != 3 && i%10 != 6)
		}
	})

	t.Run("delete if", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.DeleteIf(hexKey, func(blob.Blob) bool { return true }), "nothing stored")
		require.NoError(t, s.Put(hexKey, []byte("first"), nil, nil))
		var seen string
		require.NoError(t, s.DeleteIf(hexKey, func(b blob.Blob) bool {
			seen = string(readAll(t, b))
			return false
		}))
		require.Equal(t, "first", seen)
		requireStored(t, s, hexKey, true)
		require.NoError(t, s.DeleteIf(hexKey, func(blob.Blob) bool { return true }))
		requireStored(t, s, hexKey, false)
	})

	t.Run("concurrent writers never tear a frame", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Put(hexKey, bytes.Repeat([]byte{'0'}, 4096), nil, nil))
		var wg sync.WaitGroup
		for w := range writers {
			wg.Go(func() {
				// each writer's frame is one repeated byte, of a length all its own
				frame := bytes.Repeat([]byte("abcdefgh"[w:w+1]), 4096*(w+1))
				for range writes {
					if err := s.Put(hexKey, frame[:100], frame[100:200], frame[200:]); err != nil {
						t.Error(err)
						return
					}
				}
			})
			wg.Go(func() {
				for range writes {
					b, err := s.Open(hexKey)
					if err != nil {
						t.Error(err)
						return
					}
					frame := readAll(t, b)
					b.Close()
					if len(frame) == 0 || len(frame)%4096 != 0 ||
						!bytes.Equal(frame, bytes.Repeat(frame[:1], len(frame))) {
						t.Errorf("torn frame of %d bytes", len(frame))
						return
					}
				}
			})
		}
		wg.Wait()
	})
}

// RunMetaStoreSuite checks the blob.MetaStore that newStore returns, connected and
// empty, against the behavior every blob.MetaStore promises
func RunMetaStoreSuite(t *testing.T, newStore func(t *testing.T) blob.MetaStore) {
	read := func(t *testing.T, s blob.MetaStore, name string) string {
		t.Helper()
		r, err := s.OpenMeta(name)
		require.NoError(t, err)
		defer r.Close()
		b, err := io.ReadAll(r)
		require.NoError(t, err)
		return string(b)
	}

	t.Run("create replaces on close", func(t *testing.T) {
		s := newStore(t)
		_, err := s.OpenMeta("snapshot")
		require.ErrorIs(t, err, blob.ErrNoMeta)

		w, err := s.CreateMeta("snapshot")
		require.NoError(t, err)
		_, err = w.Write([]byte("first"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		require.Equal(t, "first", read(t, s, "snapshot"))

		w, err = s.CreateMeta("snapshot")
		require.NoError(t, err)
		_, err = w.Write([]byte("lat"))
		require.NoError(t, err)
		require.Equal(t, "first", read(t, s, "snapshot"), "unclosed writer")
		_, err = w.Write([]byte("er"))
		require.NoError(t, err)
		require.NoError(t, w.Close())
		require.Equal(t, "later", read(t, s, "snapshot"))
	})

	t.Run("large file", func(t *testing.T) {
		s := newStore(t)
		chunk := bytes.Repeat([]byte("0123456789abcdef"), 1<<12)
		w, err := s.CreateMeta("large")
		require.NoError(t, err)
		for range 64 {
			_, err = w.Write(chunk)
			require.NoError(t, err)
		}
		require.NoError(t, w.Close())
		require.Equal(t, strings.Repeat(string(chunk), 64), read(t, s, "large"))
	})

	t.Run("append", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.AppendMeta("journal", []byte("one,")))
		require.NoError(t, s.AppendMeta("journal", []byte("two,")))
		require.NoError(t, s.AppendMeta("journal", nil))
		require.NoError(t, s.AppendMeta("journal", []byte("three")))
		require.Equal(t, "one,two,three", read(t, s, "journal"))
	})

	t.Run("list and remove", func(t *testing.T) {
		s := newStore(t)
		names, err := s.ListMeta()
		require.NoError(t, err)
		require.Empty(t, names)

		require.NoError(t, s.AppendMeta("journal.2", []byte("b")))
		require.NoError(t, s.AppendMeta("journal.2", []byte("b")))
		require.NoError(t, s.AppendMeta("journal.10", []byte("c")))
		w, err := s.CreateMeta("index.1")
		require.NoError(t, err)
		require.NoError(t, w.Close())
		names, err = s.ListMeta()
		require.NoError(t, err)
		require.Equal(t, []string{"index.1", "journal.10", "journal.2"}, names)
		require.Empty(t, read(t, s, "index.1"))

		require.NoError(t, s.RemoveMeta("journal.2", "never written"))
		names, err = s.ListMeta()
		require.NoError(t, err)
		require.Equal(t, []string{"index.1", "journal.10"}, names)
		require.Equal(t, "c", read(t, s, "journal.10"))
		_, err = s.OpenMeta("journal.2")
		require.ErrorIs(t, err, blob.ErrNoMeta)
	})
}

// RunReplacementSuite checks that a scan or DeleteIf removes only the frame it looked at.
// A Store whose transactions exclude a Put meanwhile cannot run it.
func RunReplacementSuite(t *testing.T, newStore func(t *testing.T) blob.Store) {
	t.Run("a scan removes only what it looked at", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Put(hexKey, []byte("first"), nil, nil))
		// the frame is put again, from another goroutine, while the scan is looking at it
		replaced := make(chan error, 1)
		_, _, err := s.Scan("", 10, func(b blob.Blob) bool {
			require.Equal(t, "first", string(readAll(t, b)))
			go func() { replaced <- s.Put(hexKey, []byte("second"), nil, nil) }()
			require.NoError(t, <-replaced)
			return true
		})
		require.NoError(t, err)
		require.Equal(t, "second", string(get(t, s, hexKey)))

		require.NoError(t, s.DeleteIf(hexKey, func(b blob.Blob) bool {
			go func() { replaced <- s.Put(hexKey, []byte("third"), nil, nil) }()
			require.NoError(t, <-replaced)
			return true
		}))
		require.Equal(t, "third", string(get(t, s, hexKey)))
	})
}
