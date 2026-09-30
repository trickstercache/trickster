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

package bbolt

import (
	"bytes"
	"io"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	bo "github.com/trickstercache/trickster/v2/pkg/cache/bbolt/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"

	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

const (
	cacheProvider = "bbolt"
	cacheKey      = "cacheKey"
	testBucket    = "trickster_test"
)

func newConfig(dbPath string) *co.Options {
	return &co.Options{Provider: cacheProvider, BBolt: &bo.Options{Filename: dbPath, Bucket: testBucket}}
}

func newStore(t testing.TB) *Store {
	t.Helper()
	s := NewStore(t.Name(), "", "", newConfig(filepath.Join(t.TempDir(), "test.db")))
	require.NoError(t, s.Connect())
	t.Cleanup(func() { s.Close() })
	return s
}

func keep(blob.Blob) bool { return false }

func TestConformance(t *testing.T) {
	blobtest.RunStoreSuite(t, func(t *testing.T) blob.Store { return newStore(t) })
	blobtest.RunMetaStoreSuite(t, func(t *testing.T) blob.MetaStore { return newStore(t) })
}

func TestNewStoreDefaultsAndOverrides(t *testing.T) {
	s := NewStore(t.Name(), "", "", nil)
	require.Equal(t, bo.DefaultBBoltFile, s.Config.BBolt.Filename)
	require.Equal(t, bo.DefaultBBoltBucket, s.Config.BBolt.Bucket)
	require.False(t, s.Streamable())

	path := filepath.Join(t.TempDir(), "override.db")
	s = NewStore(t.Name(), path, "override_bucket", newConfig(filepath.Join(t.TempDir(), "base.db")))
	require.Equal(t, path, s.Config.BBolt.Filename)
	require.Equal(t, "override_bucket", s.Config.BBolt.Bucket)
}

func TestNew(t *testing.T) {
	c := New(t.Name(), filepath.Join(t.TempDir(), "test.db"), testBucket, nil)
	require.Equal(t, t.Name(), c.Name)
	require.Equal(t, testBucket, c.Config.BBolt.Bucket)
	require.NoError(t, c.Connect())
	require.NoError(t, c.Store(cacheKey, []byte("value"), time.Minute))
	b, _, err := c.Retrieve(cacheKey)
	require.NoError(t, err)
	require.Equal(t, "value", string(b))

	// an object is a miss once it has expired, with no reaper to have removed it
	require.NoError(t, c.Store("brief", []byte("value"), time.Millisecond))
	time.Sleep(2 * time.Millisecond)
	_, _, err = c.Retrieve("brief")
	require.ErrorIs(t, err, cache.ErrKNF)
	require.NoError(t, c.Close())
}

func TestConnectErrors(t *testing.T) {
	s := NewStore(t.Name(), "", "", newConfig(filepath.Join(t.TempDir(), "absent", "test.db")))
	require.Error(t, s.Connect())
	require.NoError(t, s.Close(), "a store that never connected")

	path := filepath.Join(t.TempDir(), "test.db")
	cfg := newConfig(path)
	cfg.BBolt.Bucket = ""
	s = NewStore(t.Name(), "", "", cfg)
	require.EqualError(t, s.Connect(), "create bucket: bucket name required")
	// the file is released, so that a corrected configuration can open it
	s = NewStore(t.Name(), "", "", newConfig(path))
	require.NoError(t, s.Connect())
	require.NoError(t, s.Close())
}

func TestUnconnected(t *testing.T) {
	stores := map[string]*Store{
		"never connected": NewStore(t.Name(), "", "", newConfig(filepath.Join(t.TempDir(), "test.db"))),
		"closed":          newStore(t),
	}
	require.NoError(t, stores["closed"].Close())
	for name, s := range stores {
		t.Run(name, func(t *testing.T) {
			require.Error(t, s.Put(cacheKey, []byte("h"), nil, nil))
			_, err := s.Open(cacheKey)
			require.Error(t, err)
			require.NotErrorIs(t, err, cache.ErrKNF)
			require.Error(t, s.Delete(cacheKey))
			next, done, err := s.Scan("after", 1, keep)
			require.Error(t, err)
			require.False(t, done)
			require.Equal(t, "after", next)

			require.Error(t, s.AppendMeta("m", nil))
			_, err = s.OpenMeta("m")
			require.Error(t, err)
			require.NotErrorIs(t, err, blob.ErrNoMeta)
			require.Error(t, s.RemoveMeta("m"))
			_, err = s.ListMeta()
			require.Error(t, err)
			w, err := s.CreateMeta("m")
			require.NoError(t, err)
			require.Error(t, w.Close())
		})
	}
}

func TestLargeFrameIsNotPooled(t *testing.T) {
	s := newStore(t)
	body := bytes.Repeat([]byte("b"), maxPooledFrame+1)
	require.NoError(t, s.Put(cacheKey, []byte("h"), nil, body))
	b, err := s.Open(cacheKey)
	require.NoError(t, err)
	require.Equal(t, int64(len(body)+1), b.Size())
	require.NoError(t, b.Close())
}

func TestBlobReadAt(t *testing.T) {
	b := &txBlob{value: []byte("0123")}
	p := make([]byte, 2)
	for _, off := range []int64{-1, 4, 5} {
		n, err := b.ReadAt(p, off)
		require.ErrorIs(t, err, io.EOF, off)
		require.Zero(t, n, off)
	}
	require.NoError(t, b.Close(), "a blob of a scan has no transaction of its own")
}

// a Blob reads what was stored when it was opened, whatever is written to its key meanwhile
func TestOpenBlobIsASnapshot(t *testing.T) {
	s := newStore(t)
	want := bytes.Repeat([]byte("v"), 1<<16)
	require.NoError(t, s.Put(cacheKey, want, nil, nil))
	b, err := s.Open(cacheKey)
	require.NoError(t, err)
	// small enough a write not to grow the file, which would wait for the Blob to close
	require.NoError(t, s.Put(cacheKey, []byte("replaced"), nil, nil))

	got := make([]byte, b.Size())
	_, err = b.ReadAt(got, 0)
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, got))
	require.NoError(t, b.Close())
}

// what Retrieve returns is the caller's own, and outlasts the transaction it was read in
func TestRetrievedBytesSurviveWrites(t *testing.T) {
	c := New(t.Name(), filepath.Join(t.TempDir(), "test.db"), testBucket, nil)
	require.NoError(t, c.Connect())
	t.Cleanup(func() { c.Close() })
	want := bytes.Repeat([]byte("v"), 1<<16)
	require.NoError(t, c.Store(cacheKey, want, time.Minute))
	got, _, err := c.Retrieve(cacheKey)
	require.NoError(t, err)

	// enough new data to grow the file, which moves where the database is mapped
	grow := bytes.Repeat([]byte("g"), 1<<20)
	for i := range 48 {
		require.NoError(t, c.Store("grow-"+strconv.Itoa(i), grow, time.Minute))
	}
	require.NoError(t, c.Store(cacheKey, []byte("replaced"), time.Minute))
	require.True(t, bytes.Equal(want, got))
}

func TestNestedBucket(t *testing.T) {
	s := newStore(t)
	const nested = "nested_bucket"
	require.NoError(t, s.dbh.Update(func(tx *bbolt.Tx) error {
		_, err := tx.Bucket(s.bucket).CreateBucket([]byte(nested))
		return err
	}))
	require.NoError(t, s.Put("a", []byte("frame"), nil, nil))

	require.Error(t, s.Delete(nested))
	_, err := s.Open(nested)
	require.ErrorIs(t, err, cache.ErrKNF)

	var n int
	_, done, err := s.Scan("", 10, func(blob.Blob) bool { n++; return false })
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, 1, n, "a nested bucket is no frame")
}

// a frame put in a key's place after a scan looked at it is not the frame the scan removes
func TestScanRemovesOnlyWhatItLookedAt(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Put(cacheKey, []byte("first"), nil, nil))
	require.NoError(t, s.Put("same length", []byte("first"), nil, nil))
	looked := []fingerprint{fingerprintOf(cacheKey, []byte("first")), fingerprintOf("same length", []byte("first"))}
	require.NoError(t, s.Put(cacheKey, []byte("second"), nil, nil))
	require.NoError(t, s.deleteMatching(looked))
	b, err := s.Open(cacheKey)
	require.NoError(t, err)
	require.NoError(t, b.Close())
	_, err = s.Open("same length")
	require.ErrorIs(t, err, cache.ErrKNF)

	long := bytes.Repeat([]byte("x"), 2*fingerprintLen)
	require.NoError(t, s.Put(cacheKey, long, nil, nil))
	fp := fingerprintOf(cacheKey, long)
	require.Len(t, fp.head, fingerprintLen)
	require.True(t, fp.matches(long))
	require.False(t, fp.matches(nil))
	require.False(t, fp.matches(long[:len(long)-1]))
	require.NoError(t, s.deleteMatching(nil))
}

func TestScanHoldsNoTransactionAcrossCalls(t *testing.T) {
	s := newStore(t)
	for i := range 5 {
		require.NoError(t, s.Put("key-"+strconv.Itoa(i), []byte("frame"), nil, nil))
	}
	after, done, err := s.Scan("", 2, func(blob.Blob) bool { return true })
	require.NoError(t, err)
	require.False(t, done)
	require.Equal(t, "key-1", after)
	require.Zero(t, s.dbh.Stats().OpenTxN)
	_, err = s.Open("key-0")
	require.ErrorIs(t, err, cache.ErrKNF, "marked frames are removed once the read ends")
}

func TestMetaNames(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"", "a\x00b", "\x00"} {
		_, err := s.CreateMeta(name)
		require.ErrorIs(t, err, ErrInvalidMetaName)
		require.ErrorIs(t, s.AppendMeta(name, nil), ErrInvalidMetaName)
		_, err = s.OpenMeta(name)
		require.ErrorIs(t, err, ErrInvalidMetaName)
		require.ErrorIs(t, s.RemoveMeta(name), ErrInvalidMetaName)
	}
}

func TestMetaSegments(t *testing.T) {
	s := newStore(t)
	// files whose names begin alike keep their segments apart
	require.NoError(t, s.AppendMeta("journal", []byte("a")))
	require.NoError(t, s.AppendMeta("journal.1", []byte("b")))
	require.NoError(t, s.AppendMeta("journal", []byte("c")))
	require.NoError(t, s.AppendMeta("j", []byte("d")))
	// a key that is no segment is passed over
	require.NoError(t, s.dbh.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(s.meta).Put([]byte("stray"), []byte("x"))
	}))

	read := func(name string) string {
		r, err := s.OpenMeta(name)
		require.NoError(t, err)
		defer r.Close()
		b, err := io.ReadAll(r)
		require.NoError(t, err)
		return string(b)
	}
	require.Equal(t, "ac", read("journal"))
	require.Equal(t, "b", read("journal.1"))
	require.Equal(t, "d", read("j"))
	names, err := s.ListMeta()
	require.NoError(t, err)
	require.Equal(t, []string{"j", "journal", "journal.1"}, names)

	// content past one segment's size is split, and read back whole
	w, err := s.CreateMeta("journal")
	require.NoError(t, err)
	large := bytes.Repeat([]byte("0123456789"), segmentSize/4)
	_, err = w.Write(large)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.Equal(t, string(large), read("journal"))
	require.Equal(t, "b", read("journal.1"))
	require.Zero(t, s.dbh.Stats().OpenTxN)
}

func TestMetaNestedBucket(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.dbh.Update(func(tx *bbolt.Tx) error {
		_, err := tx.Bucket(s.meta).CreateBucket(segmentKey([]byte("index\x00"), 0))
		return err
	}))
	require.Error(t, s.RemoveMeta("index"))
	w, err := s.CreateMeta("index")
	require.NoError(t, err)
	require.Error(t, w.Close())
}

func BenchmarkStore(b *testing.B) {
	s := newStore(b)
	hdr, body := make([]byte, 108), make([]byte, 1024)
	require.NoError(b, s.Put(cacheKey, hdr, nil, body))
	b.Run("open", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			f, err := s.Open(cacheKey)
			if err != nil {
				b.Fatal(err)
			}
			f.Close()
		}
	})
	b.Run("open/miss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s.Open("absent")
		}
	})
}
