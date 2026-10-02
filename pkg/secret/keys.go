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
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"os"
	"strings"
	"sync"
)

// MinKeyBytes is the shortest key an operator may configure: 256 bits.
const MinKeyBytes = 32

// MinTagBytes and MaxTagBytes bound the tag a Signer makes: at least 128 bits, at most a
// whole HMAC-SHA256.
const (
	MinTagBytes = 16
	MaxTagBytes = sha256.Size
)

var (
	// ErrKeyTooShort is returned for a key shorter than MinKeyBytes.
	ErrKeyTooShort = fmt.Errorf("a key must be at least %d bytes", MinKeyBytes)
	// ErrUnexpandedKey is returned for an inline key that still holds a ${...} reference; used
	// as it is, it would key every token with text that anyone can read.
	ErrUnexpandedKey = errors.New("a key cannot hold an unexpanded ${...} reference")
	// ErrKeyAndFile is returned when a key is given both inline and as a file.
	ErrKeyAndFile = errors.New("set a key inline or as a file, not both")
	// ErrNoPurpose is returned when a Signer or Sealer is asked for without a purpose label.
	ErrNoPurpose = errors.New("a key's purpose label cannot be empty")
	// ErrTagSize is returned for a Signer tag size outside MinTagBytes-MaxTagBytes.
	ErrTagSize = fmt.Errorf("a tag must be %d to %d bytes", MinTagBytes, MaxTagBytes)
	// ErrOpen is returned for a sealed message that no key opens: altered, sealed for another
	// purpose or other associated data, or sealed with a key that has since been dropped.
	ErrOpen = errors.New("message could not be opened")
)

// unexpanded is how an environment reference begins
const unexpanded = "${"

// ReadKey returns the key held inline or in the named file, whose trailing line breaks are
// trimmed; it returns nil when neither is set.
func ReadKey(inline Secret, file string) ([]byte, error) {
	switch {
	case inline != "" && file != "":
		return nil, ErrKeyAndFile
	case inline != "":
		// only an inline key is checked for a reference: a file's key may be random bytes
		if strings.Contains(string(inline), unexpanded) {
			return nil, ErrUnexpandedKey
		}
		return checkLength([]byte(inline))
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return checkLength(bytes.TrimRight(b, "\r\n"))
	}
	return nil, nil
}

func checkLength(key []byte) ([]byte, error) {
	if len(key) < MinKeyBytes {
		return nil, fmt.Errorf("%w, not %d", ErrKeyTooShort, len(key))
	}
	return key, nil
}

// processKey is the key used when none is configured; it lasts as long as the process, so a
// config reload does not invalidate what was made with it
var processKey = sync.OnceValue(func() []byte {
	key := make([]byte, MinKeyBytes)
	// crypto/rand.Read never returns an error: it crashes the program if it cannot read
	_, _ = rand.Read(key)
	return key
})

// Keyring is the keys that tokens are made and checked with, newest first: tokens are made with
// the first, and one made with any of them is accepted, so that a key can be rotated in.
type Keyring struct {
	keys      [][]byte
	ephemeral bool
}

// NewKeyring returns a keyring of the keys, newest first. With none, it holds a random key that
// lasts as long as the process, and Ephemeral reports true.
func NewKeyring(keys ...[]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return &Keyring{keys: [][]byte{processKey()}, ephemeral: true}, nil
	}
	k := &Keyring{keys: make([][]byte, len(keys))}
	for i, key := range keys {
		if _, err := checkLength(key); err != nil {
			return nil, err
		}
		k.keys[i] = bytes.Clone(key)
	}
	return k, nil
}

// LoadKeyring returns a keyring of the key held inline or in the named file, or of the
// process's random key when neither is set.
func LoadKeyring(inline Secret, file string) (*Keyring, error) {
	key, err := ReadKey(inline, file)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return NewKeyring()
	}
	return NewKeyring(key)
}

// Ephemeral reports whether the keyring holds the process's random key, so that what is made
// with it is neither accepted after a restart nor by another replica.
func (k *Keyring) Ephemeral() bool {
	return k.ephemeral
}

// subkeyBytes is the size of a purpose's key: an AES-256 key, and a whole SHA-256 block's worth
// of HMAC key
const subkeyBytes = 32

// derive returns each key's subkey for the purpose, newest first, so that one operator key can
// serve several features without a token made for one being accepted by another
func (k *Keyring) derive(purpose string) ([][]byte, error) {
	if purpose == "" {
		return nil, ErrNoPurpose
	}
	subs := make([][]byte, len(k.keys))
	for i, key := range k.keys {
		sub, err := hkdf.Key(sha256.New, key, nil, purpose, subkeyBytes)
		if err != nil {
			return nil, err
		}
		subs[i] = sub
	}
	return subs, nil
}

// Signer makes and checks HMAC-SHA256 tags, truncated to a fixed size, for one purpose.
// It is safe for concurrent use.
type Signer struct {
	size int
	macs []sync.Pool
}

// mac is a keyed HMAC and the room to sum it into, reused across tags
type mac struct {
	h   hash.Hash
	sum [sha256.Size]byte
}

// Signer returns a signer whose tags are tagBytes long, keyed for the purpose. A purpose label
// names the feature and the version of its format, such as "trickster/alb-sticky/v1".
func (k *Keyring) Signer(purpose string, tagBytes int) (*Signer, error) {
	if tagBytes < MinTagBytes || tagBytes > MaxTagBytes {
		return nil, ErrTagSize
	}
	subs, err := k.derive(purpose)
	if err != nil {
		return nil, err
	}
	s := &Signer{size: tagBytes, macs: make([]sync.Pool, len(subs))}
	for i, sub := range subs {
		s.macs[i].New = func() any { return &mac{h: hmac.New(sha256.New, sub)} }
	}
	return s, nil
}

// TagSize is how many bytes a tag is.
func (s *Signer) TagSize() int {
	return s.size
}

// Sum appends the tag of the message, which is the parts in order, made with the newest key.
func (s *Signer) Sum(dst []byte, parts ...[]byte) []byte {
	m := s.macs[0].Get().(*mac)
	dst = append(dst, m.tag(parts)[:s.size]...)
	s.macs[0].Put(m)
	return dst
}

// Verify reports whether tag is the tag of the message, which is the parts in order, made with
// any of the keys. Each comparison takes constant time.
func (s *Signer) Verify(tag []byte, parts ...[]byte) bool {
	if len(tag) != s.size {
		return false
	}
	for i := range s.macs {
		m := s.macs[i].Get().(*mac)
		ok := hmac.Equal(m.tag(parts)[:s.size], tag)
		s.macs[i].Put(m)
		if ok {
			return true
		}
	}
	return false
}

func (m *mac) tag(parts [][]byte) []byte {
	m.h.Reset()
	for _, p := range parts {
		m.h.Write(p)
	}
	return m.h.Sum(m.sum[:0])
}

// Sealer encrypts and authenticates messages for one purpose with AES-256-GCM and a random
// nonce. It is safe for concurrent use.
type Sealer struct {
	aeads []cipher.AEAD
}

// Sealer returns a sealer keyed for the purpose. A purpose label names the feature and the
// version of its format, such as "trickster/oidc-session/v1/<authenticator>".
func (k *Keyring) Sealer(purpose string) (*Sealer, error) {
	subs, err := k.derive(purpose)
	if err != nil {
		return nil, err
	}
	s := &Sealer{aeads: make([]cipher.AEAD, len(subs))}
	for i, sub := range subs {
		block, err := aes.NewCipher(sub)
		if err != nil {
			return nil, err
		}
		if s.aeads[i], err = cipher.NewGCMWithRandomNonce(block); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Overhead is how many bytes longer a sealed message is than the message: its nonce and tag.
func (s *Sealer) Overhead() int {
	return s.aeads[0].Overhead()
}

// Seal appends the message, encrypted with the newest key and bound to the associated data,
// to dst. The associated data is not in the output; Open must be given the same.
func (s *Sealer) Seal(dst, msg, ad []byte) []byte {
	return s.aeads[0].Seal(dst, nil, msg, ad)
}

// Open appends the message that sealed holds to dst, when any of the keys opens it with the
// associated data; otherwise it returns ErrOpen.
func (s *Sealer) Open(dst, sealed, ad []byte) ([]byte, error) {
	for _, a := range s.aeads {
		if msg, err := a.Open(dst, nil, sealed, ad); err == nil {
			return msg, nil
		}
	}
	return nil, ErrOpen
}
