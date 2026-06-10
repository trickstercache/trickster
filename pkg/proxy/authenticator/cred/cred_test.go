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

package cred

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return b
}

func randomPassword(t *testing.T) string {
	t.Helper()
	return base64.RawStdEncoding.EncodeToString(randomBytes(t, 16))
}

func randomSalt(t *testing.T, n int) string {
	t.Helper()
	b := randomBytes(t, n)
	for i, v := range b {
		b[i] = cryptSaltAlphabet[int(v)%len(cryptSaltAlphabet)]
	}
	return string(b)
}

func generateMD5CryptHash(password, magicPrefix, salt string) string {
	pwBytes := []byte(password)
	saltBytes := []byte(salt)
	pwLen := len(pwBytes)
	b := md5.New()
	b.Write(pwBytes)
	b.Write(saltBytes)
	b.Write(pwBytes)
	bsum := b.Sum(nil)
	a := md5.New()
	a.Write(pwBytes)
	a.Write([]byte(magicPrefix))
	a.Write(saltBytes)
	cnt := pwLen
	for ; cnt > 16; cnt -= 16 {
		a.Write(bsum)
	}
	a.Write(bsum[0:cnt])
	for cnt = pwLen; cnt > 0; cnt >>= 1 {
		if (cnt & 1) == 0 {
			a.Write(pwBytes[0:1])
		} else {
			a.Write([]byte{0})
		}
	}
	asum := a.Sum(nil)
	csum := asum
	for round := range 1000 {
		c := md5.New()
		if (round & 1) != 0 {
			c.Write(pwBytes)
		} else {
			c.Write(csum)
		}
		if (round % 3) != 0 {
			c.Write(saltBytes)
		}
		if (round % 7) != 0 {
			c.Write(pwBytes)
		}
		if (round & 1) == 0 {
			c.Write(pwBytes)
		} else {
			c.Write(csum)
		}
		csum = c.Sum(nil)
	}
	reordered := []byte{
		csum[12], csum[6], csum[0],
		csum[13], csum[7], csum[1],
		csum[14], csum[8], csum[2],
		csum[15], csum[9], csum[3],
		csum[5], csum[10], csum[4],
		csum[11],
	}
	magic := strings.TrimPrefix(magicPrefix, "$")
	magic = strings.TrimSuffix(magic, "$")
	return fmt.Sprintf("$%s$%s$%s", magic, salt, apr1Base64Encode(reordered))
}

func generateSHACryptHash(
	password, magicPrefix, salt string,
	rounds int,
	hashFunc func() hash.Hash,
	hashSize int,
) string {
	key := []byte(password)
	saltBytes := []byte(salt)
	keyLen := len(key)
	saltLen := len(saltBytes)
	if saltLen > 16 {
		saltBytes = saltBytes[:16]
		saltLen = 16
	}
	b := hashFunc()
	b.Write(key)
	b.Write(saltBytes)
	b.Write(key)
	bsum := b.Sum(nil)
	a := hashFunc()
	a.Write(key)
	a.Write(saltBytes)
	cnt := keyLen
	for ; cnt > hashSize; cnt -= hashSize {
		a.Write(bsum)
	}
	a.Write(bsum[0:cnt])
	for cnt = keyLen; cnt > 0; cnt >>= 1 {
		if (cnt & 1) != 0 {
			a.Write(bsum)
		} else {
			a.Write(key)
		}
	}
	asum := a.Sum(nil)
	p := hashFunc()
	for range keyLen {
		p.Write(key)
	}
	psum := p.Sum(nil)
	pseq := make([]byte, 0, keyLen)
	for cnt = keyLen; cnt > hashSize; cnt -= hashSize {
		pseq = append(pseq, psum...)
	}
	pseq = append(pseq, psum[0:cnt]...)
	s := hashFunc()
	for range 16 + int(asum[0]) {
		s.Write(saltBytes)
	}
	ssum := s.Sum(nil)
	sseq := make([]byte, 0, saltLen)
	for cnt = saltLen; cnt > hashSize; cnt -= hashSize {
		sseq = append(sseq, ssum...)
	}
	sseq = append(sseq, ssum[0:cnt]...)
	csum := asum
	for round := range rounds {
		c := hashFunc()
		if (round & 1) != 0 {
			c.Write(pseq)
		} else {
			c.Write(csum)
		}
		if (round % 3) != 0 {
			c.Write(sseq)
		}
		if (round % 7) != 0 {
			c.Write(pseq)
		}
		if (round & 1) != 0 {
			c.Write(csum)
		} else {
			c.Write(pseq)
		}
		csum = c.Sum(nil)
	}
	var reordered []byte
	if hashSize == 32 {
		reordered = []byte{
			csum[20], csum[10], csum[0],
			csum[11], csum[1], csum[21],
			csum[2], csum[22], csum[12],
			csum[23], csum[13], csum[3],
			csum[14], csum[4], csum[24],
			csum[5], csum[25], csum[15],
			csum[26], csum[16], csum[6],
			csum[17], csum[7], csum[27],
			csum[8], csum[28], csum[18],
			csum[29], csum[19], csum[9],
			csum[30], csum[31],
		}
	} else {
		reordered = []byte{
			csum[42], csum[21], csum[0],
			csum[1], csum[43], csum[22],
			csum[23], csum[2], csum[44],
			csum[45], csum[24], csum[3],
			csum[4], csum[46], csum[25],
			csum[26], csum[5], csum[47],
			csum[48], csum[27], csum[6],
			csum[7], csum[49], csum[28],
			csum[29], csum[8], csum[50],
			csum[51], csum[30], csum[9],
			csum[10], csum[52], csum[31],
			csum[32], csum[11], csum[53],
			csum[54], csum[33], csum[12],
			csum[13], csum[55], csum[34],
			csum[35], csum[14], csum[56],
			csum[57], csum[36], csum[15],
			csum[16], csum[58], csum[37],
			csum[38], csum[17], csum[59],
			csum[60], csum[39], csum[18],
			csum[19], csum[61], csum[40],
			csum[41], csum[20], csum[62],
			csum[63],
		}
	}
	magic := strings.TrimSuffix(strings.TrimPrefix(magicPrefix, "$"), "$")
	encoded := apr1Base64Encode(reordered)
	if rounds != 5000 {
		return fmt.Sprintf("$%s$rounds=%d$%s$%s", magic, rounds, salt, encoded)
	}
	return fmt.Sprintf("$%s$%s$%s", magic, salt, encoded)
}

func generateBcryptHash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	return string(h)
}

type credentialCase struct {
	name     string
	generate func(t *testing.T, password string) string
}

func TestVerifyPassword_SupportedFormats(t *testing.T) {
	t.Parallel()

	cases := []credentialCase{
		{
			name: "plaintext",
			generate: func(_ *testing.T, password string) string {
				return password
			},
		},
		{
			name: "md5-crypt",
			generate: func(_ *testing.T, password string) string {
				return generateMD5CryptHash(password, "$1$", randomSalt(t, 8))
			},
		},
		{
			name: "apr1",
			generate: func(_ *testing.T, password string) string {
				return generateMD5CryptHash(password, "$apr1$", randomSalt(t, 8))
			},
		},
		{
			name: "sha256-crypt",
			generate: func(_ *testing.T, password string) string {
				return generateSHACryptHash(password, "$5$", randomSalt(t, 8), 5000, sha256.New, 32)
			},
		},
		{
			name: "sha256-crypt-with-rounds",
			generate: func(_ *testing.T, password string) string {
				return generateSHACryptHash(password, "$5$", randomSalt(t, 8), 8000, sha256.New, 32)
			},
		},
		{
			name: "sha512-crypt",
			generate: func(_ *testing.T, password string) string {
				return generateSHACryptHash(password, "$6$", randomSalt(t, 8), 5000, sha512.New, 64)
			},
		},
		{
			name: "bcrypt",
			generate: func(t *testing.T, password string) string {
				return generateBcryptHash(t, password)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			password := randomPassword(t)
			hash := tc.generate(t, password)

			if err := VerifyPassword(hash, password); err != nil {
				t.Fatalf("VerifyPassword(valid): %v", err)
			}

			wrong := randomPassword(t)
			for wrong == password {
				wrong = randomPassword(t)
			}
			if err := VerifyPassword(hash, wrong); err == nil {
				t.Fatal("expected error for wrong password")
			}
		})
	}
}

func TestVerifyPassword_ErrUnauthorized(t *testing.T) {
	t.Parallel()

	hash := randomPassword(t)
	wrong := randomPassword(t)
	for wrong == hash {
		wrong = randomPassword(t)
	}

	if err := VerifyPassword(hash, wrong); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestVerifyPassword_BcryptWrongPassword(t *testing.T) {
	t.Parallel()

	password := randomPassword(t)
	hash := generateBcryptHash(t, password)
	wrong := randomPassword(t)
	for wrong == password {
		wrong = randomPassword(t)
	}

	err := VerifyPassword(hash, wrong)
	if err == nil {
		t.Fatal("expected bcrypt mismatch error")
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected bcrypt error, got ErrUnauthorized")
	}
}

func TestVerifyPassword_InvalidHashFormats(t *testing.T) {
	t.Parallel()

	password := randomPassword(t)
	cases := []struct {
		name string
		hash string
	}{
		{name: "md5-too-few-parts", hash: "$1$onlysalt"},
		{name: "sha256-too-few-parts", hash: "$5$onlysalt"},
		{name: "sha256-bad-rounds", hash: "$5$rounds=abc$" + randomSalt(t, 8) + "$deadbeef"},
		{name: "sha256-missing-hash-with-rounds", hash: "$5$rounds=5000$" + randomSalt(t, 8)},
		{name: "sha512-missing-hash", hash: "$6$" + randomSalt(t, 8)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := VerifyPassword(tc.hash, password); err == nil {
				t.Fatal("expected error for invalid hash format")
			}
		})
	}
}

func TestVerifyPassword_SHACryptCustomRounds(t *testing.T) {
	t.Parallel()

	password := randomPassword(t)
	hash := generateSHACryptHash(password, "$5$", randomSalt(t, 8), 8000, sha256.New, 32)
	if err := VerifyPassword(hash, password); err != nil {
		t.Fatalf("VerifyPassword(custom rounds): %v", err)
	}
}

func TestApr1Base64Encode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "three-bytes",
			input:    []byte{0x00, 0x00, 0x00},
			expected: "....",
		},
		{
			name:     "two-bytes",
			input:    []byte{0x00, 0x00},
			expected: "...",
		},
		{
			name:     "one-byte",
			input:    []byte{0x00},
			expected: "..",
		},
		{
			name:     "five-bytes",
			input:    []byte{0x01, 0x02, 0x03, 0x04, 0x05},
			expected: "/6k.2I.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := apr1Base64Encode(tc.input); got != tc.expected {
				t.Fatalf("apr1Base64Encode() = %q, want %q", got, tc.expected)
			}
		})
	}
}
