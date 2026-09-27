/*
 * Copyright 2026 The Trickster Authors
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

package secret

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	purposeA = "trickster/test-a/v1"
	purposeB = "trickster/test-b/v1"
)

var (
	keyOld = []byte(strings.Repeat("o", MinKeyBytes))
	keyNew = []byte(strings.Repeat("n", MinKeyBytes))
	msg    = []byte("the message")
)

func keyring(t *testing.T, keys ...[]byte) *Keyring {
	t.Helper()
	k, err := NewKeyring(keys...)
	require.NoError(t, err)
	return k
}

func signer(t *testing.T, k *Keyring, purpose string) *Signer {
	t.Helper()
	s, err := k.Signer(purpose, MinTagBytes)
	require.NoError(t, err)
	return s
}

func sealer(t *testing.T, k *Keyring, purpose string) *Sealer {
	t.Helper()
	s, err := k.Sealer(purpose)
	require.NoError(t, err)
	return s
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestReadKey(t *testing.T) {
	long := strings.Repeat("k", MinKeyBytes)

	key, err := ReadKey(Secret(long), "")
	require.NoError(t, err)
	require.Equal(t, long, string(key))

	key, err = ReadKey("", "")
	require.NoError(t, err)
	require.Nil(t, key, "no key configured is not an error")

	_, err = ReadKey(Secret(long[1:]), "")
	require.ErrorIs(t, err, ErrKeyTooShort)

	// the loader does not expand references, so this text would be the key
	_, err = ReadKey(Secret("${TRICKSTER_STICKY_SECRET}"+long), "")
	require.ErrorIs(t, err, ErrUnexpandedKey)

	_, err = ReadKey(Secret(long), writeFile(t, long))
	require.ErrorIs(t, err, ErrKeyAndFile)

	for _, trailer := range []string{"", "\n", "\r\n", "\n\n"} {
		key, err = ReadKey("", writeFile(t, long+trailer))
		require.NoError(t, err)
		require.Equal(t, long, string(key), "trailer %q", trailer)
	}

	// a file may hold anything, a reference-like pair of bytes in a random key included
	key, err = ReadKey("", writeFile(t, "${"+long))
	require.NoError(t, err)
	require.Equal(t, "${"+long, string(key))

	_, err = ReadKey("", writeFile(t, long[1:]+"\n"))
	require.ErrorIs(t, err, ErrKeyTooShort, "the line break does not count toward the length")

	_, err = ReadKey("", filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewKeyring(t *testing.T) {
	k := keyring(t)
	require.True(t, k.Ephemeral())
	// the process's key is the same for every keyring, so a reload keeps its tokens
	tag := signer(t, k, purposeA).Sum(nil, msg)
	require.True(t, signer(t, keyring(t), purposeA).Verify(tag, msg))

	configured := keyring(t, keyNew)
	require.False(t, configured.Ephemeral())
	require.False(t, signer(t, configured, purposeA).Verify(tag, msg))

	_, err := NewKeyring(keyNew, keyOld[1:])
	require.ErrorIs(t, err, ErrKeyTooShort)

	// the keyring keeps its own copy
	key := bytes.Clone(keyNew)
	k = keyring(t, key)
	tag = signer(t, k, purposeA).Sum(nil, msg)
	key[0] ^= 0xff
	require.True(t, signer(t, k, purposeA).Verify(tag, msg))
}

func TestLoadKeyring(t *testing.T) {
	k, err := LoadKeyring(Secret(keyNew), "")
	require.NoError(t, err)
	require.False(t, k.Ephemeral())
	tag := signer(t, k, purposeA).Sum(nil, msg)

	k, err = LoadKeyring("", writeFile(t, string(keyNew)+"\n"))
	require.NoError(t, err)
	require.True(t, signer(t, k, purposeA).Verify(tag, msg), "a key read from a file is the same key")

	k, err = LoadKeyring("", "")
	require.NoError(t, err)
	require.True(t, k.Ephemeral())

	_, err = LoadKeyring("short", "")
	require.ErrorIs(t, err, ErrKeyTooShort)
}

// A tag is pinned for a fixed key, purpose and message: replicas must agree on it, and a change
// to how a purpose's key is derived would otherwise invalidate every token already issued.
func TestSignerGolden(t *testing.T) {
	s, err := keyring(t, keyNew).Signer(purposeA, MaxTagBytes)
	require.NoError(t, err)
	require.Equal(t, "99f1aa0d9925ebb7be2f5ea8d705a9344fc5adcad4260beb419c288838b4ed5a",
		hex.EncodeToString(s.Sum(nil, msg)))
}

func TestSigner(t *testing.T) {
	k := keyring(t, keyNew)
	for _, size := range []int{MinTagBytes - 1, MaxTagBytes + 1, 0} {
		_, err := k.Signer(purposeA, size)
		require.ErrorIs(t, err, ErrTagSize, "size %d", size)
	}
	_, err := k.Signer("", MinTagBytes)
	require.ErrorIs(t, err, ErrNoPurpose)

	s := signer(t, k, purposeA)
	require.Equal(t, MinTagBytes, s.TagSize())
	tag := s.Sum([]byte("prefix:"), msg)
	require.Equal(t, "prefix:", string(tag[:7]), "Sum appends")
	tag = tag[7:]
	require.Len(t, tag, MinTagBytes)
	require.True(t, s.Verify(tag, msg))

	// the message is the parts in order, whatever their boundaries
	require.True(t, s.Verify(tag, msg[:3], msg[3:]))
	require.True(t, s.Verify(tag, nil, msg))

	require.False(t, s.Verify(tag, []byte("another message")))
	require.False(t, s.Verify(tag[:len(tag)-1], msg), "a shorter tag is not accepted")
	require.False(t, s.Verify(append(bytes.Clone(tag), 0), msg), "nor a longer one")
	for i := range tag {
		altered := bytes.Clone(tag)
		altered[i] ^= 1
		require.False(t, s.Verify(altered, msg), "byte %d altered", i)
	}

	// a tag made for one purpose is not accepted for another
	require.False(t, signer(t, k, purposeB).Verify(tag, msg))
	require.NotEqual(t, tag, signer(t, k, purposeB).Sum(nil, msg))

	// a longer tag is the same HMAC, less truncated
	whole, err := k.Signer(purposeA, MaxTagBytes)
	require.NoError(t, err)
	require.Equal(t, tag, whole.Sum(nil, msg)[:MinTagBytes])
}

func TestSignerRotation(t *testing.T) {
	old := signer(t, keyring(t, keyOld), purposeA)
	rotated := signer(t, keyring(t, keyNew, keyOld), purposeA)
	current := signer(t, keyring(t, keyNew), purposeA)

	fromOld := old.Sum(nil, msg)
	require.True(t, rotated.Verify(fromOld, msg), "a rotated-in key keeps accepting the old one's tags")
	require.False(t, current.Verify(fromOld, msg), "until the old key is dropped")

	fromRotated := rotated.Sum(nil, msg)
	require.Equal(t, current.Sum(nil, msg), fromRotated, "tags are made with the newest key")
	require.False(t, old.Verify(fromRotated, msg))
}

func TestSignerConcurrent(t *testing.T) {
	s := signer(t, keyring(t, keyNew, keyOld), purposeA)
	want := s.Sum(nil, msg)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				if !bytes.Equal(s.Sum(nil, msg), want) || !s.Verify(want, msg) {
					t.Error("concurrent use changed a tag")
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestSealer(t *testing.T) {
	k := keyring(t, keyNew)
	_, err := k.Sealer("")
	require.ErrorIs(t, err, ErrNoPurpose)

	s := sealer(t, k, purposeA)
	ad := []byte("cookie-v1")
	sealed := s.Seal([]byte("prefix:"), msg, ad)
	require.Equal(t, "prefix:", string(sealed[:7]), "Seal appends")
	sealed = sealed[7:]
	require.Len(t, sealed, len(msg)+s.Overhead())
	require.NotContains(t, string(sealed), string(msg))
	require.NotEqual(t, sealed, s.Seal(nil, msg, ad), "each seal has its own nonce")

	out, err := s.Open([]byte("out:"), sealed, ad)
	require.NoError(t, err)
	require.Equal(t, "out:"+string(msg), string(out), "Open appends")

	_, err = s.Open(nil, sealed, []byte("cookie-v2"))
	require.ErrorIs(t, err, ErrOpen, "the associated data is bound")
	_, err = sealer(t, k, purposeB).Open(nil, sealed, ad)
	require.ErrorIs(t, err, ErrOpen, "as is the purpose")
	for i := range sealed {
		altered := bytes.Clone(sealed)
		altered[i] ^= 1
		_, err = s.Open(nil, altered, ad)
		require.ErrorIs(t, err, ErrOpen, "byte %d altered", i)
	}
	_, err = s.Open(nil, sealed[:s.Overhead()-1], ad)
	require.ErrorIs(t, err, ErrOpen)
}

func TestSealerRotation(t *testing.T) {
	old := sealer(t, keyring(t, keyOld), purposeA)
	rotated := sealer(t, keyring(t, keyNew, keyOld), purposeA)
	current := sealer(t, keyring(t, keyNew), purposeA)

	fromOld := old.Seal(nil, msg, nil)
	out, err := rotated.Open(nil, fromOld, nil)
	require.NoError(t, err)
	require.Equal(t, msg, out)
	_, err = current.Open(nil, fromOld, nil)
	require.ErrorIs(t, err, ErrOpen)

	fromRotated := rotated.Seal(nil, msg, nil)
	out, err = current.Open(nil, fromRotated, nil)
	require.NoError(t, err, "messages are sealed with the newest key")
	require.Equal(t, msg, out)
	_, err = old.Open(nil, fromRotated, nil)
	require.ErrorIs(t, err, ErrOpen)
}

func BenchmarkSignerVerify(b *testing.B) {
	k, _ := NewKeyring(keyNew)
	s, _ := k.Signer(purposeA, MinTagBytes)
	payload := bytes.Repeat([]byte{7}, 26)
	tag := s.Sum(nil, payload)
	b.ReportAllocs()
	for b.Loop() {
		s.Verify(tag, payload)
	}
}
