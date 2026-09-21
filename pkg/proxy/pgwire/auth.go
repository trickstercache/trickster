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
package pgwire

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- PostgreSQL's legacy md5 authentication method is defined in terms of MD5.
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"

	"github.com/jackc/pgx/v5/pgproto3"
)

const (
	// maxAuthMessageLen is PostgreSQL's PG_MAX_AUTH_TOKEN_LENGTH.
	maxAuthMessageLen = 65535
	md5SaltLen        = 4
)

var (
	errAuthFailed = errors.New("authentication failed")
	// errNoAuthMethod means the stored credential needs a method this
	// connection may not use, e.g. a cleartext password without TLS.
	errNoAuthMethod = errors.New("no permitted authentication method")

	// passwordHashPrefixes identify crypt-style hashes, which can only be
	// checked against a password the client sends in the clear.
	passwordHashPrefixes = []string{"$apr1$", "$1$", "$5$", "$6$", "$2a$", "$2b$", "$2y$"}
)

type authEntry struct {
	scram  *cred.SCRAMVerifier
	stored string
	md5    bool
}

func newAuthEntries(users map[string]string) (map[string]*authEntry, error) {
	entries := make(map[string]*authEntry, len(users))
	for user, credential := range users {
		entry := &authEntry{}
		switch {
		case cred.IsSCRAMVerifier(credential):
			verifier, err := cred.ParseSCRAMVerifier(credential)
			if err != nil {
				return nil, err
			}
			entry.scram = verifier
		case cred.IsPostgresMD5(credential):
			entry.stored, entry.md5 = credential, true
		case isPasswordHash(credential):
			entry.stored = credential
		default:
			salt := make([]byte, cred.SCRAMSaltLen)
			if _, err := rand.Read(salt); err != nil {
				return nil, err
			}
			verifier, err := cred.NewSCRAMVerifier(credential, salt, cred.DefaultSCRAMIterations)
			if err != nil {
				return nil, err
			}
			entry.scram = verifier
		}
		entries[user] = entry
	}
	return entries, nil
}

func isPasswordHash(credential string) bool {
	for _, prefix := range passwordHashPrefixes {
		if strings.HasPrefix(credential, prefix) {
			return true
		}
	}
	return false
}

func (s *session) authenticate() error {
	// runs the strongest method the user's stored credential allows.
	// Unknown users get a mock SCRAM exchange, so they are indistinguishable.
	entry := s.front.auth[s.user]
	switch {
	case entry == nil:
		return s.authenticateSCRAM(&scramServer{verifier: mockVerifier(s.front.mockSecret, s.user)})
	case entry.scram != nil:
		return s.authenticateSCRAM(&scramServer{verifier: entry.scram, known: true})
	case entry.md5 && s.server.config.AllowMD5:
		return s.authenticateMD5(entry.stored)
	case s.secure || s.server.config.AllowCleartextWithoutTLS:
		return s.authenticateCleartext(entry.stored)
	}
	return errNoAuthMethod
}

func (s *session) authenticateSCRAM(exchange *scramServer) error {
	if s.secure {
		exchange.binding = tlsServerEndPoint(s.servedCertificate)
	}
	if err := s.send(&pgproto3.AuthenticationSASL{AuthMechanisms: exchange.mechanisms()}); err != nil {
		return err
	}
	body, err := s.readAuthMessage()
	if err != nil {
		return err
	}
	var initial pgproto3.SASLInitialResponse
	if err = initial.Decode(body); err != nil {
		return errAuthFailed
	}
	serverFirst, err := exchange.first(initial.AuthMechanism, initial.Data)
	if err != nil {
		return errAuthFailed
	}
	if err = s.send(&pgproto3.AuthenticationSASLContinue{Data: serverFirst}); err != nil {
		return err
	}
	if body, err = s.readAuthMessage(); err != nil {
		return err
	}
	serverFinal, err := exchange.final(body)
	if err != nil {
		return errAuthFailed
	}
	return s.send(&pgproto3.AuthenticationSASLFinal{Data: serverFinal})
}

func (s *session) authenticateCleartext(stored string) error {
	if err := s.send(&pgproto3.AuthenticationCleartextPassword{}); err != nil {
		return err
	}
	body, err := s.readAuthMessage()
	if err != nil {
		return err
	}
	password, _, ok := bytes.Cut(body, []byte{0})
	if !ok || cred.VerifyUserPassword(s.user, stored, string(password)) != nil {
		return errAuthFailed
	}
	return nil
}

func (s *session) authenticateMD5(stored string) error {
	var salt [md5SaltLen]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	if err := s.send(&pgproto3.AuthenticationMD5Password{Salt: salt}); err != nil {
		return err
	}
	body, err := s.readAuthMessage()
	if err != nil {
		return err
	}
	response, _, ok := bytes.Cut(body, []byte{0})
	// The client sends md5(hex(md5(password + user)) + salt); the stored
	// verifier is that inner hex digest behind an "md5" prefix.
	inner := stored[len(cred.PostgresMD5Prefix):]
	sum := md5.Sum(append([]byte(inner), salt[:]...)) // #nosec G401 -- method defined by PostgreSQL
	expected := cred.PostgresMD5Prefix + hex.EncodeToString(sum[:])
	if !ok || subtle.ConstantTimeCompare(response, []byte(expected)) != 1 {
		return errAuthFailed
	}
	return nil
}

func (s *session) readAuthMessage() ([]byte, error) {
	typ, body, err := readFrame(s.client, maxAuthMessageLen)
	if err != nil {
		return nil, err
	}
	if typ != msgPassword {
		return nil, io.ErrUnexpectedEOF
	}
	return body, nil
}
