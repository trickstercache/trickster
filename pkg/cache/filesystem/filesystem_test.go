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

package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob"
	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	flo "github.com/trickstercache/trickster/v2/pkg/cache/filesystem/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"

	"github.com/stretchr/testify/require"
)

const (
	cacheProvider = "filesystem"
	digestKey     = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	nativeKey     = "prefix|tenant:dpc:d41d8cd98f00b204e9800998ecf8427e"
)

func newStore(t testing.TB) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache", cacheProvider)
	s := NewStore(t.Name(), &co.Options{Provider: cacheProvider, Filesystem: &flo.Options{CachePath: dir}})
	require.NoError(t, s.Connect())
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func (s *Store) path(key string) string {
	p, _ := s.appendPath(nil, key)
	return string(p)
}

func keep(blob.Blob) bool { return false }

func TestConformance(t *testing.T) {
	blobtest.RunStoreSuite(t, func(t *testing.T) blob.Store { return newStore(t) })
	blobtest.RunReplacementSuite(t, func(t *testing.T) blob.Store { return newStore(t) })
	blobtest.RunMetaStoreSuite(t, func(t *testing.T) blob.MetaStore { return newStore(t) })
}

func TestNewCache(t *testing.T) {
	cfg := &co.Options{Filesystem: &flo.Options{CachePath: filepath.Join(t.TempDir(), "cache")}}
	c := NewCache(t.Name(), cfg)
	require.Equal(t, t.Name(), c.Name)
	require.Same(t, cfg, c.Config)
	require.NoError(t, c.Connect())
	require.NoError(t, c.Store(digestKey, []byte("value"), time.Minute))
	b, _, err := c.Retrieve(digestKey)
	require.NoError(t, err)
	require.Equal(t, "value", string(b))

	// an object is a miss once it has expired, with no reaper to have removed it
	require.NoError(t, c.Store(nativeKey, []byte("value"), time.Millisecond))
	time.Sleep(2 * time.Millisecond)
	_, _, err = c.Retrieve(nativeKey)
	require.ErrorIs(t, err, cache.ErrKNF)
	require.NoError(t, c.Close())
}

func TestConnect(t *testing.T) {
	s := newStore(t)
	require.True(t, s.Streamable())
	entries, err := os.ReadDir(s.root)
	require.NoError(t, err)
	require.Empty(t, entries, "the write probe is removed")

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, fileMode))
	s = NewStore(t.Name(), &co.Options{Filesystem: &flo.Options{CachePath: filepath.Join(file, "cache")}})
	require.ErrorContains(t, s.Connect(), "directory is not writeable by trickster")
}

func TestIsDigest(t *testing.T) {
	tests := []struct {
		name, key string
		want      bool
	}{
		{"sha256", digestKey, true},
		{"md5", digestKey[:32], true},
		{"31 characters", digestKey[:31], false},
		{"33 characters", digestKey[:33], false},
		{"63 characters", digestKey[:63], false},
		{"65 characters", digestKey + "0", false},
		{"uppercase", strings.ToUpper(digestKey), false},
		{"one uppercase", digestKey[:63] + "A", false},
		{"not hex", digestKey[:63] + "g", false},
		{"alphanumeric", strings.Repeat("z", 32), false},
		{"below the digits", digestKey[:63] + "/", false},
		{"between the digits and letters", digestKey[:63] + ":", false},
		{"separator", digestKey[:31] + string(separator), false},
		{"empty", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, isDigest(test.key))
		})
	}
}

func TestPath(t *testing.T) {
	s := newStore(t)
	sep := string(separator)

	require.Equal(t, s.root+sep+"9f"+sep+"86"+sep+digestKey, s.path(digestKey))
	require.Equal(t, s.root+sep+"9f"+sep+"86"+sep+digestKey[:32], s.path(digestKey[:32]))

	seen := map[string]string{}
	for _, key := range append(blobtest.Keys, strings.Repeat("k", keyBufferSize+1), digestKey+".chunk") {
		path := s.path(key)
		rel, err := filepath.Rel(s.root, path)
		require.NoError(t, err)
		parts := strings.Split(rel, sep)
		require.Len(t, parts, 3, key)
		require.True(t, isShard(parts[0]) && isShard(parts[1]), key)
		require.True(t, isDigest(parts[2]), "%s is named %s", key, parts[2])
		require.Equal(t, parts[2][:4], parts[0]+parts[1], key)
		if !isDigest(key) {
			require.Len(t, parts[2], 2*hashedBytes, key)
		}
		require.Empty(t, seen[path], "%s shares a file with %s", key, seen[path])
		seen[path] = key
		require.Equal(t, path, s.path(key), "a key always has the same path")
	}
	// keys alike but for their case are hashed, and so have files apart on any filesystem
	require.NotEqual(t, strings.ToLower(s.path(strings.ToUpper(digestKey))), s.path(digestKey))
}

func TestPutLeavesNoTemp(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Put(digestKey, []byte("h"), []byte("m"), []byte("b")))
	entries, err := os.ReadDir(filepath.Dir(s.path(digestKey)))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, digestKey, entries[0].Name())
	fi, err := entries[0].Info()
	require.NoError(t, err)
	require.Equal(t, os.FileMode(fileMode), fi.Mode().Perm())
}

func TestPutErrors(t *testing.T) {
	t.Run("directory cannot be made", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, os.WriteFile(filepath.Join(s.root, "9f"), nil, fileMode))
		require.Error(t, s.Put(digestKey, []byte("h"), nil, nil))
	})
	t.Run("rename fails", func(t *testing.T) {
		s := newStore(t)
		// a directory that is not empty stands where the file belongs
		require.NoError(t, os.MkdirAll(filepath.Join(s.path(digestKey), "child"), dirMode))
		require.Error(t, s.Put(digestKey, []byte("h"), nil, nil))
		entries, err := os.ReadDir(filepath.Dir(s.path(digestKey)))
		require.NoError(t, err)
		require.Len(t, entries, 1, "the temporary file is removed")

		require.Error(t, s.Delete(nativeKey, digestKey), "a failed removal is reported")
	})
}

func TestOpenErrors(t *testing.T) {
	s := newStore(t)
	_, err := s.Open(digestKey)
	require.ErrorIs(t, err, cache.ErrKNF)

	if os.Geteuid() == 0 {
		t.Skip("a privileged user can read any file")
	}
	require.NoError(t, s.Put(digestKey, []byte("h"), nil, nil))
	require.NoError(t, os.Chmod(s.path(digestKey), 0))
	_, err = s.Open(digestKey)
	require.Error(t, err)
	require.NotErrorIs(t, err, cache.ErrKNF, "a file that cannot be read is not a miss")
}

func TestWriteSections(t *testing.T) {
	tests := []struct {
		skip int
		want string
	}{
		{0, "headmetabody"}, {2, "admetabody"}, {4, "metabody"}, {6, "tabody"}, {8, "body"}, {11, "y"}, {12, ""},
	}
	for _, test := range tests {
		path := filepath.Join(t.TempDir(), "f")
		f, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, writeSections(f, test.skip, []byte("head"), []byte("meta"), []byte("body")))
		require.NoError(t, f.Close())
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, test.want, string(got))
		require.Error(t, writeSections(f, test.skip, []byte("head"), []byte("meta"), []byte("body!")), "closed file")
	}
}

func TestWriteFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, writeFrame(f, nil, nil, nil))
	require.NoError(t, writeFrame(f, []byte("h"), nil, []byte("b")))
	require.NoError(t, f.Close())
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hb", string(got))
	require.Error(t, writeFrame(f, []byte("h"), nil, nil), "closed file")
}

func TestScanClearsWhatIsNotAFrame(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Put(digestKey, []byte("frame"), nil, nil))
	require.NoError(t, s.AppendMeta("journal", []byte("j")))
	dir := filepath.Dir(s.path(digestKey))

	// what a cache of an earlier layout leaves in the cache path itself
	legacy := filepath.Join(s.root, "9f86d081884c7d65~4chunkdata")
	require.NoError(t, os.WriteFile(legacy, []byte("legacy"), fileMode))
	// what a write that never finished leaves behind, lately and long ago
	fresh, stale := filepath.Join(dir, tempPrefix+"fresh"), filepath.Join(dir, tempPrefix+"stale")
	require.NoError(t, os.WriteFile(fresh, []byte("torn"), fileMode))
	require.NoError(t, os.WriteFile(stale, []byte("torn"), fileMode))
	old := time.Now().Add(-2 * tempMaxAge)
	require.NoError(t, os.Chtimes(stale, old, old))
	// directories that are no part of the layout
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "other", "deep"), dirMode))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), dirMode))
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "9f", "zz"), dirMode))

	var seen []string
	next, done, err := s.Scan("", 10, func(b blob.Blob) bool {
		p := make([]byte, b.Size())
		b.ReadAt(p, 0)
		seen = append(seen, string(p))
		return false
	})
	require.NoError(t, err)
	require.True(t, done)
	require.Equal(t, "9f/86/"+digestKey, next)
	require.Equal(t, []string{"frame"}, seen, "neither temporary nor metadata files are frames")

	require.NoFileExists(t, legacy)
	require.NoFileExists(t, stale)
	require.FileExists(t, fresh, "a write may still be using it")
	names, err := s.ListMeta()
	require.NoError(t, err)
	require.Equal(t, []string{"journal"}, names)
}

func TestScanResumes(t *testing.T) {
	s := newStore(t)
	// three directories, the second of them holding three frames
	keys := []string{
		"00aa" + digestKey[4:], "00bb" + digestKey[4:32], "00bb" + digestKey[4:], "00bb" + digestKey[4:63] + "f",
		"11aa" + digestKey[4:],
	}
	for _, key := range keys {
		require.NoError(t, s.Put(key, []byte(key), nil, nil))
	}
	for limit := 1; limit <= len(keys)+1; limit++ {
		var seen []string
		var after string
		var done bool
		var err error
		for calls := 0; !done; calls++ {
			require.LessOrEqual(t, calls, len(keys), "limit %d", limit)
			after, done, err = s.Scan(after, limit, func(b blob.Blob) bool {
				p := make([]byte, b.Size())
				b.ReadAt(p, 0)
				seen = append(seen, string(p))
				return false
			})
			require.NoError(t, err)
		}
		require.Equal(t, keys, seen, "limit %d", limit)
	}
}

func TestScanErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a privileged user can read any directory")
	}
	unreadable := func(t *testing.T, path string) {
		t.Helper()
		require.NoError(t, os.Chmod(path, 0))
		t.Cleanup(func() { os.Chmod(path, dirMode) })
	}
	t.Run("cache path", func(t *testing.T) {
		s := newStore(t)
		unreadable(t, s.root)
		_, _, err := s.Scan("", 1, keep)
		require.Error(t, err, "pruning")
		_, _, err = s.Scan("00/00/0", 1, keep)
		require.Error(t, err, "listing")
	})
	t.Run("cache path is gone", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, os.RemoveAll(s.root))
		_, done, err := s.Scan("00/00/0", 1, keep)
		require.NoError(t, err)
		require.True(t, done)
	})
	for name, depth := range map[string]int{"outer directory": 2, "inner directory": 1} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			require.NoError(t, s.Put(digestKey, []byte("h"), nil, nil))
			dir := s.path(digestKey)
			for range depth {
				dir = filepath.Dir(dir)
			}
			unreadable(t, dir)
			_, _, err := s.Scan("", 1, keep)
			require.Error(t, err)
		})
	}
	t.Run("file", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, s.Put(digestKey, []byte("h"), nil, nil))
		require.NoError(t, os.Chmod(s.path(digestKey), 0))
		var visited bool
		_, done, err := s.Scan("", 1, func(blob.Blob) bool { visited = true; return true })
		require.NoError(t, err)
		require.True(t, done)
		require.False(t, visited, "a file that cannot be opened is passed over")
		require.FileExists(t, s.path(digestKey))
	})
}

func TestMetaNames(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"", ".hidden", "../escape", "a/b", `a\b`, "..", tempPrefix + "1"} {
		_, err := s.CreateMeta(name)
		require.ErrorIs(t, err, ErrInvalidMetaName, name)
		require.ErrorIs(t, s.AppendMeta(name, nil), ErrInvalidMetaName, name)
		_, err = s.OpenMeta(name)
		require.ErrorIs(t, err, ErrInvalidMetaName, name)
		require.ErrorIs(t, s.RemoveMeta(name), ErrInvalidMetaName, name)
	}
}

func TestMetaErrors(t *testing.T) {
	t.Run("directory cannot be made", func(t *testing.T) {
		s := newStore(t)
		require.NoError(t, os.WriteFile(filepath.Join(s.root, metaDir), nil, fileMode))
		_, err := s.CreateMeta("index")
		require.Error(t, err)
		require.Error(t, s.AppendMeta("journal", []byte("j")))
		_, err = s.OpenMeta("index")
		require.Error(t, err)
		require.NotErrorIs(t, err, blob.ErrNoMeta)
		_, err = s.ListMeta()
		require.Error(t, err)
	})
	t.Run("write and rename fail", func(t *testing.T) {
		s := newStore(t)
		w, err := s.CreateMeta("index")
		require.NoError(t, err)
		require.NoError(t, w.(*metaWriter).File.Close())
		_, err = w.Write([]byte("lost"))
		require.Error(t, err)
		require.Error(t, w.Close())
		_, err = s.OpenMeta("index")
		require.ErrorIs(t, err, blob.ErrNoMeta, "a failed write replaces nothing")

		// a directory that is not empty stands where the file belongs
		require.NoError(t, os.MkdirAll(filepath.Join(s.root, metaDir, "index", "child"), dirMode))
		w, err = s.CreateMeta("index")
		require.NoError(t, err)
		require.Error(t, w.Close())
		require.Error(t, s.RemoveMeta("index"))
		require.Error(t, s.AppendMeta("index", nil))
		names, err := s.ListMeta()
		require.NoError(t, err)
		require.Empty(t, names, "neither directories nor temporary files are listed")
	})
}

func BenchmarkAppendPath(b *testing.B) {
	s := newStore(b)
	for name, key := range map[string]string{"digest": digestKey, "hashed": nativeKey} {
		b.Run(name, func(b *testing.B) {
			var pb [pathBufferSize]byte
			b.ReportAllocs()
			for b.Loop() {
				s.appendPath(pb[:0], key)
			}
		})
	}
}

func BenchmarkStore(b *testing.B) {
	s := newStore(b)
	hdr, body := make([]byte, 108), make([]byte, 1024)
	for name, key := range map[string]string{"digest": digestKey, "hashed": nativeKey} {
		b.Run("put/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := s.Put(key, hdr, nil, body); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("open/"+name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				f, err := s.Open(key)
				if err != nil {
					b.Fatal(err)
				}
				f.Close()
			}
		})
	}
	b.Run("open/miss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			s.Open("absent")
		}
	})
}
