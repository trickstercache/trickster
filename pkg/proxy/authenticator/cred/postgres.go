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
	"crypto/hmac"
	"crypto/md5" // #nosec G501 -- PostgreSQL's legacy md5 verifier format is defined in terms of MD5.
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"golang.org/x/text/secure/precis"
)

const (
	// SCRAMSHA256Prefix begins a PostgreSQL SCRAM-SHA-256 verifier, as stored in pg_authid.
	SCRAMSHA256Prefix = "SCRAM-SHA-256$"
	// PostgresMD5Prefix begins a PostgreSQL md5 verifier: md5 + hex(md5(password + user)).
	PostgresMD5Prefix = "md5"
	// DefaultSCRAMIterations is PostgreSQL's default scram_iterations.
	DefaultSCRAMIterations = 4096
	// SCRAMSaltLen is PostgreSQL's SCRAM salt length in bytes.
	SCRAMSaltLen = 16

	postgresMD5Len     = len(PostgresMD5Prefix) + 2*md5.Size
	scramClientKeyText = "Client Key"
	scramServerKeyText = "Server Key"
)

// ErrInvalidSCRAMVerifier is returned for a malformed SCRAM-SHA-256 verifier.
var ErrInvalidSCRAMVerifier = errors.New("invalid SCRAM-SHA-256 verifier")

// SCRAMVerifier holds the server-side secrets of a SCRAM-SHA-256 credential.
type SCRAMVerifier struct {
	Iterations int
	Salt       []byte
	StoredKey  []byte
	ServerKey  []byte
}

// IsSCRAMVerifier reports whether s is formatted as a PostgreSQL SCRAM-SHA-256 verifier.
func IsSCRAMVerifier(s string) bool {
	return strings.HasPrefix(s, SCRAMSHA256Prefix)
}

// ParseSCRAMVerifier parses SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>.
func ParseSCRAMVerifier(s string) (*SCRAMVerifier, error) {
	rest, ok := strings.CutPrefix(s, SCRAMSHA256Prefix)
	if !ok {
		return nil, ErrInvalidSCRAMVerifier
	}
	params, keys, ok := strings.Cut(rest, "$")
	if !ok {
		return nil, ErrInvalidSCRAMVerifier
	}
	iterations, salt, ok := strings.Cut(params, ":")
	if !ok {
		return nil, ErrInvalidSCRAMVerifier
	}
	storedKey, serverKey, ok := strings.Cut(keys, ":")
	if !ok {
		return nil, ErrInvalidSCRAMVerifier
	}
	v := &SCRAMVerifier{}
	var err error
	if v.Iterations, err = strconv.Atoi(iterations); err != nil || v.Iterations <= 0 {
		return nil, ErrInvalidSCRAMVerifier
	}
	if v.Salt, err = base64.StdEncoding.DecodeString(salt); err != nil || len(v.Salt) == 0 {
		return nil, ErrInvalidSCRAMVerifier
	}
	if v.StoredKey, err = base64.StdEncoding.DecodeString(storedKey); err != nil || len(v.StoredKey) != sha256.Size {
		return nil, ErrInvalidSCRAMVerifier
	}
	if v.ServerKey, err = base64.StdEncoding.DecodeString(serverKey); err != nil || len(v.ServerKey) != sha256.Size {
		return nil, ErrInvalidSCRAMVerifier
	}
	return v, nil
}

// NewSCRAMVerifier derives a verifier from a plaintext password, a non-empty
// salt and a positive iteration count.
func NewSCRAMVerifier(password string, salt []byte, iterations int) (*SCRAMVerifier, error) {
	if iterations <= 0 || len(salt) == 0 {
		return nil, ErrInvalidSCRAMVerifier
	}
	salted, err := pbkdf2.Key(sha256.New, saslPrep(password), salt, iterations, sha256.Size)
	if err != nil {
		return nil, err
	}
	clientKey := scramHMAC(salted, scramClientKeyText)
	storedKey := sha256.Sum256(clientKey)
	return &SCRAMVerifier{
		Iterations: iterations, Salt: salt,
		StoredKey: storedKey[:], ServerKey: scramHMAC(salted, scramServerKeyText),
	}, nil
}

// String renders the verifier in PostgreSQL's pg_authid format.
func (v *SCRAMVerifier) String() string {
	enc := base64.StdEncoding.EncodeToString
	return SCRAMSHA256Prefix + strconv.Itoa(v.Iterations) + ":" + enc(v.Salt) + "$" +
		enc(v.StoredKey) + ":" + enc(v.ServerKey)
}

// VerifyPassword reports whether password derives this verifier's stored key.
func (v *SCRAMVerifier) VerifyPassword(password string) bool {
	derived, err := NewSCRAMVerifier(password, v.Salt, v.Iterations)
	return err == nil && subtle.ConstantTimeCompare(derived.StoredKey, v.StoredKey) == 1
}

// IsPostgresMD5 reports whether s is formatted as a PostgreSQL md5 verifier.
func IsPostgresMD5(s string) bool {
	if len(s) != postgresMD5Len || !strings.HasPrefix(s, PostgresMD5Prefix) {
		return false
	}
	_, err := hex.DecodeString(s[len(PostgresMD5Prefix):])
	return err == nil
}

// PostgresMD5 returns the PostgreSQL md5 verifier for a user and password.
func PostgresMD5(user, password string) string {
	sum := md5.Sum([]byte(password + user)) // #nosec G401 -- format defined by PostgreSQL
	return PostgresMD5Prefix + hex.EncodeToString(sum[:])
}

// VerifyUserPassword is VerifyPassword plus the PostgreSQL md5 verifier, which is salted by user.
func VerifyUserPassword(user, hash, password string) error {
	if IsPostgresMD5(hash) {
		if subtle.ConstantTimeCompare([]byte(PostgresMD5(user, password)), []byte(hash)) == 1 {
			return nil
		}
		return ErrUnauthorized
	}
	return VerifyPassword(hash, password)
}

func verifySCRAMHash(hash, password string) error {
	v, err := ParseSCRAMVerifier(hash)
	if err != nil {
		return err
	}
	if !v.VerifyPassword(password) {
		return ErrUnauthorized
	}
	return nil
}

func scramHMAC(key []byte, text string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(text))
	return mac.Sum(nil)
}

func saslPrep(password string) string {
	// normalizes as PostgreSQL does: passwords SASLprep rejects are used as given.
	if prepared, err := precis.OpaqueString.String(password); err == nil {
		return prepared
	}
	return password
}
