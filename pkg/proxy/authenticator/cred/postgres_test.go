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
	"errors"
	"testing"
)

const (
	pgTestUser     = "grafana_ro"
	pgTestPassword = "correct horse battery staple"
	pgTestSalt     = "0123456789abcdef"
	// pgTestServerVerifier is pg_authid.rolpassword as written by PostgreSQL
	// 18.6 for the password in pgTestServerPassword.
	pgTestServerVerifier = "SCRAM-SHA-256$4096:WNfFZKFHvROmdmbbjx+PQw==$" +
		"rXwYcbfgoWqLPHd58+DCgRPLPHCK3zT7Te9jmMOlaec=:Wj8OsS1QZ7R6cdtoDUXXzGrzNKnBVEkDvvp48NiVcoQ="
	pgTestServerPassword = "pencil"
	pgTestValidSaltB64   = "c2FsdA=="
)

func TestSCRAMVerifierRoundTrip(t *testing.T) {
	v, err := NewSCRAMVerifier(pgTestPassword, []byte(pgTestSalt), DefaultSCRAMIterations)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSCRAMVerifier(v.String()) {
		t.Fatalf("unexpected format %q", v.String())
	}
	parsed, err := ParseSCRAMVerifier(v.String())
	if err != nil {
		t.Fatal(err)
	}
	if parsed.String() != v.String() || parsed.Iterations != DefaultSCRAMIterations {
		t.Fatalf("round trip changed the verifier: %q != %q", parsed, v)
	}
	if !parsed.VerifyPassword(pgTestPassword) || parsed.VerifyPassword(pgTestPassword+"x") {
		t.Fatal("VerifyPassword gave the wrong answer")
	}
	if err = VerifyPassword(v.String(), pgTestPassword); err != nil {
		t.Fatal(err)
	}
	if err = VerifyPassword(v.String(), "wrong"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestSCRAMVerifierInteroperatesWithPostgres(t *testing.T) {
	if err := VerifyPassword(pgTestServerVerifier, pgTestServerPassword); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(pgTestServerVerifier, pgTestServerPassword+"s"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestParseSCRAMVerifierRejectsMalformed(t *testing.T) {
	prefix := SCRAMSHA256Prefix + "4096:" + pgTestValidSaltB64
	for name, value := range map[string]string{
		"no prefix":      "plaintext",
		"no keys":        prefix,
		"no salt":        SCRAMSHA256Prefix + "4096$a:b",
		"no server key":  prefix + "$onlyone",
		"bad iterations": SCRAMSHA256Prefix + "x:" + pgTestValidSaltB64 + "$a:b",
		"zero iters":     SCRAMSHA256Prefix + "0:" + pgTestValidSaltB64 + "$a:b",
		"bad salt":       SCRAMSHA256Prefix + "4096:!!!$a:b",
		"bad stored key": prefix + "$!!!:b",
		"short stored":   prefix + "$" + pgTestValidSaltB64 + ":" + pgTestValidSaltB64,
		"bad server key": pgTestServerVerifier[:len(pgTestServerVerifier)-4] + "!!!!",
	} {
		if _, err := ParseSCRAMVerifier(value); !errors.Is(err, ErrInvalidSCRAMVerifier) {
			t.Fatalf("%s: expected ErrInvalidSCRAMVerifier, got %v", name, err)
		}
	}
	if err := VerifyPassword(SCRAMSHA256Prefix+"junk", pgTestPassword); !errors.Is(err, ErrInvalidSCRAMVerifier) {
		t.Fatalf("expected ErrInvalidSCRAMVerifier, got %v", err)
	}
	if _, err := NewSCRAMVerifier(pgTestPassword, []byte(pgTestSalt), 0); err == nil {
		t.Fatal("expected zero iterations to be rejected")
	}
	if (&SCRAMVerifier{Salt: []byte(pgTestSalt)}).VerifyPassword(pgTestPassword) {
		t.Fatal("a verifier that cannot be derived must not verify")
	}
}

func TestSASLPrepFallsBackToRawPassword(t *testing.T) {
	// SASLprep prohibits control characters; PostgreSQL then uses the raw bytes.
	prohibited := "bell" + string(rune(7))
	if saslPrep(prohibited) != prohibited {
		t.Fatal("a password SASLprep rejects must be used as given")
	}
	if saslPrep(pgTestPassword) != pgTestPassword {
		t.Fatal("an ordinary password must be unchanged")
	}
}

func TestPostgresMD5(t *testing.T) {
	verifier := PostgresMD5(pgTestUser, pgTestPassword)
	if !IsPostgresMD5(verifier) {
		t.Fatalf("unexpected format %q", verifier)
	}
	for _, notMD5 := range []string{
		"", PostgresMD5Prefix, PostgresMD5Prefix + "short",
		"xyz" + verifier[len(PostgresMD5Prefix):], PostgresMD5Prefix + "zz" + verifier[len(PostgresMD5Prefix)+2:],
	} {
		if IsPostgresMD5(notMD5) {
			t.Fatalf("%q must not be recognized as an md5 verifier", notMD5)
		}
	}
	if err := VerifyUserPassword(pgTestUser, verifier, pgTestPassword); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUserPassword("someone_else", verifier, pgTestPassword); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("the md5 verifier is salted by user; got %v", err)
	}
	if err := VerifyUserPassword(pgTestUser, pgTestPassword, pgTestPassword); err != nil {
		t.Fatalf("non-md5 credentials must fall through to VerifyPassword: %v", err)
	}
}
