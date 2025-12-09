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
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

var ErrUnauthorized = errors.New("unauthorized")

// VerifyPassword verifies a password against a stored hash
// Supported formats: apr1 crypt, md5 crypt, bcrypt, sha-256 crypt, sha-512 crypt
func VerifyPassword(hash, password string) error {
	if hash == password {
		return nil
	}
	if strings.HasPrefix(hash, "$apr1$") {
		return verifyMD5CryptHash(hash, password, "$apr1$")
	}
	if strings.HasPrefix(hash, "$1$") {
		return verifyMD5CryptHash(hash, password, "$1$")
	}
	if strings.HasPrefix(hash, "$5$") {
		return verifySHA256CryptHash(hash, password)
	}
	if strings.HasPrefix(hash, "$6$") {
		return verifySHA512CryptHash(hash, password)
	}
	if strings.HasPrefix(hash, "$2a$") ||
		strings.HasPrefix(hash, "$2b$") || strings.HasPrefix(hash, "$2y$") {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	}
	if hash == password { // finally assume unencrypted and do a direct compare
		return nil
	}
	return ErrUnauthorized
}

// verifyMD5CryptHash verifies a password against an MD5-Crypt hash (supports both $apr1$ and $1$)
func verifyMD5CryptHash(hash, password, magicPrefix string) error {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 {
		return errors.New("invalid MD5-Crypt hash format")
	}
	magic := parts[1]
	expectedMagic := strings.TrimPrefix(magicPrefix, "$")
	expectedMagic = strings.TrimSuffix(expectedMagic, "$")
	if magic != expectedMagic {
		return fmt.Errorf("expected %s got %s", expectedMagic, magic)
	}
	salt := parts[2]
	expectedHash := parts[3]
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
	encoded := apr1Base64Encode(reordered)
	if encoded != expectedHash {
		return errors.New("password mismatch")
	}
	return nil
}

// verifySHA256CryptHash verifies a password against a SHA-256 Crypt hash
func verifySHA256CryptHash(hash, password string) error {
	return verifySHACryptHash(hash, password, "$5$", sha256.New, 32)
}

// verifySHA512CryptHash verifies a password against a SHA-512 Crypt hash
func verifySHA512CryptHash(hash, password string) error {
	return verifySHACryptHash(hash, password, "$6$", sha512.New, 64)
}

// verifySHACryptHash verifies SHA-256 or SHA-512 Crypt hashes
func verifySHACryptHash(
	hash, password, magicPrefix string,
	hashFunc func() hash.Hash, hashSize int,
) error {
	parts := strings.Split(hash, "$")
	if len(parts) < 4 {
		return errors.New("invalid SHA-Crypt hash format")
	}
	if parts[1] != strings.TrimPrefix(magicPrefix, "$") {
		return errors.New("hash magic prefix mismatch")
	}
	var salt string
	rounds := 5000
	roundsStr, hasRounds := strings.CutPrefix(parts[2], "rounds=")
	if hasRounds {
		var err error
		rounds, err = strconv.Atoi(roundsStr)
		if err != nil {
			return fmt.Errorf("invalid rounds: %w", err)
		}
		if rounds < 1000 {
			rounds = 1000
		} else if rounds > 999999999 {
			rounds = 999999999
		}
		salt = parts[3]
		if len(parts) < 5 {
			return errors.New("invalid SHA-Crypt hash format: missing hash")
		}
		expectedHash := parts[4]
		return computeAndCompareSHACrypt(password, salt, rounds, expectedHash, hashFunc, hashSize)
	}
	salt = parts[2]
	if len(parts) < 4 {
		return errors.New("invalid SHA-Crypt hash format: missing hash")
	}
	expectedHash := parts[3]
	return computeAndCompareSHACrypt(password, salt, rounds, expectedHash, hashFunc, hashSize)
}

func computeAndCompareSHACrypt(password, salt string, rounds int, expectedHash string, hashFunc func() hash.Hash, hashSize int) error {
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
		// SHA-256: 32 bytes reordered to 32 bytes
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
		// SHA-512: 64 bytes reordered to 64 bytes
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
	encoded := apr1Base64Encode(reordered)
	if encoded != expectedHash {
		return errors.New("password mismatch")
	}
	return nil
}

// apr1Base64Encode encodes the MD5 hash using Apache's base64 variant (Hash64)
func apr1Base64Encode(data []byte) string {
	const alphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	hashSize := (len(data) * 8) / 6
	if (len(data)*8)%6 != 0 {
		hashSize++
	}
	result := make([]byte, hashSize)
	src := data
	dst := result
	for len(src) > 0 {
		switch len(src) {
		default:
			// Process 3 bytes -> 4 characters
			dst[0] = alphabet[src[0]&0x3f]
			dst[1] = alphabet[((src[0]>>6)|(src[1]<<2))&0x3f]
			dst[2] = alphabet[((src[1]>>4)|(src[2]<<4))&0x3f]
			dst[3] = alphabet[(src[2]>>2)&0x3f]
			src = src[3:]
			dst = dst[4:]
		case 2:
			// Process 2 bytes -> 3 characters
			dst[0] = alphabet[src[0]&0x3f]
			dst[1] = alphabet[((src[0]>>6)|(src[1]<<2))&0x3f]
			dst[2] = alphabet[(src[1]>>4)&0x3f]
			src = src[2:]
			dst = dst[3:]
		case 1:
			// Process 1 byte -> 2 characters
			dst[0] = alphabet[src[0]&0x3f]
			dst[1] = alphabet[(src[0]>>6)&0x3f]
			src = src[1:]
			dst = dst[2:]
		}
	}
	return string(result)
}
