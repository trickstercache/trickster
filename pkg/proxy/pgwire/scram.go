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
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
)

const (
	mechSCRAMSHA256     = "SCRAM-SHA-256"
	mechSCRAMSHA256Plus = "SCRAM-SHA-256-PLUS"
	// bindingTLSServerEndPoint is the only channel binding type PostgreSQL defines.
	bindingTLSServerEndPoint = "tls-server-end-point"
	scramNonceLen            = 18

	gs2NoBinding        = "n,,"
	gs2ClientOnlyNoBind = "y,,"
	gs2BindingPrefix    = "p="
)

var errSCRAM = errors.New("SCRAM exchange failed")

type scramServer struct {
	verifier *cred.SCRAMVerifier
	known    bool
	binding  []byte

	plus            bool
	gs2Header       string
	clientFirstBare string
	serverFirst     string
	nonce           string
}

func (s *scramServer) mechanisms() []string {
	// lists what to advertise: channel binding only when TLS can supply it.
	if s.binding != nil {
		return []string{mechSCRAMSHA256Plus, mechSCRAMSHA256}
	}
	return []string{mechSCRAMSHA256}
}

func (s *scramServer) first(mechanism string, clientFirst []byte) ([]byte, error) {
	switch mechanism {
	case mechSCRAMSHA256Plus:
		if s.binding == nil {
			return nil, errSCRAM
		}
		s.plus = true
	case mechSCRAMSHA256:
	default:
		return nil, errSCRAM
	}
	message := string(clientFirst)
	var bare string
	switch {
	case strings.HasPrefix(message, gs2NoBinding):
		s.gs2Header, bare = gs2NoBinding, message[len(gs2NoBinding):]
	case strings.HasPrefix(message, gs2ClientOnlyNoBind):
		// The client believes the server cannot bind. If binding was
		// advertised, a man in the middle stripped it (RFC 5802 section 6).
		if s.binding != nil {
			return nil, errSCRAM
		}
		s.gs2Header, bare = gs2ClientOnlyNoBind, message[len(gs2ClientOnlyNoBind):]
	case strings.HasPrefix(message, gs2BindingPrefix):
		header, rest, ok := strings.Cut(message, ",,")
		if !ok || header != gs2BindingPrefix+bindingTLSServerEndPoint {
			return nil, errSCRAM
		}
		s.gs2Header, bare = header+",,", rest
	default:
		return nil, errSCRAM
	}
	if s.plus != strings.HasPrefix(s.gs2Header, gs2BindingPrefix) {
		return nil, errSCRAM
	}
	clientNonce := scramAttribute(bare, 'r')
	if clientNonce == "" {
		return nil, errSCRAM
	}
	raw := make([]byte, scramNonceLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	s.clientFirstBare = bare
	s.nonce = clientNonce + base64.RawStdEncoding.EncodeToString(raw)
	s.serverFirst = "r=" + s.nonce + ",s=" + base64.StdEncoding.EncodeToString(s.verifier.Salt) +
		",i=" + strconv.Itoa(s.verifier.Iterations)
	return []byte(s.serverFirst), nil
}

func (s *scramServer) final(clientFinal []byte) ([]byte, error) {
	message := string(clientFinal)
	before, after, ok := strings.CutLast(message, ",p=")
	if !ok {
		return nil, errSCRAM
	}
	withoutProof := before
	proof, err := base64.StdEncoding.DecodeString(after)
	if err != nil || len(proof) != sha256.Size {
		return nil, errSCRAM
	}
	expectedBinding := base64.StdEncoding.EncodeToString(append([]byte(s.gs2Header), s.bindingData()...))
	if scramAttribute(withoutProof, 'c') != expectedBinding || scramAttribute(withoutProof, 'r') != s.nonce {
		return nil, errSCRAM
	}
	authMessage := []byte(s.clientFirstBare + "," + s.serverFirst + "," + withoutProof)
	signature := hmacSHA256(s.verifier.StoredKey, authMessage)
	clientKey := make([]byte, sha256.Size)
	subtle.XORBytes(clientKey, proof, signature)
	storedKey := sha256.Sum256(clientKey)
	if !s.known || subtle.ConstantTimeCompare(storedKey[:], s.verifier.StoredKey) != 1 {
		return nil, errSCRAM
	}
	serverSignature := hmacSHA256(s.verifier.ServerKey, authMessage)
	return []byte("v=" + base64.StdEncoding.EncodeToString(serverSignature)), nil
}

func (s *scramServer) bindingData() []byte {
	if s.plus {
		return s.binding
	}
	return nil
}

func scramAttribute(message string, name byte) string {
	for message != "" {
		var field string
		field, message, _ = strings.Cut(message, ",")
		if len(field) >= 2 && field[0] == name && field[1] == '=' {
			return field[2:]
		}
	}
	return ""
}

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

func mockVerifier(serverSecret []byte, user string) *cred.SCRAMVerifier {
	// returns a stable, unguessable verifier for an unknown user, so
	// repeated probes see a consistent salt exactly as they would for a real user.
	salt := hmacSHA256(serverSecret, []byte("salt:"+user))[:cred.SCRAMSaltLen]
	return &cred.SCRAMVerifier{
		Iterations: cred.DefaultSCRAMIterations, Salt: salt,
		StoredKey: hmacSHA256(serverSecret, []byte("stored:"+user)),
		ServerKey: hmacSHA256(serverSecret, []byte("server:"+user)),
	}
}

func tlsServerEndPoint(certificate *tls.Certificate) []byte {
	// hashes the server certificate per RFC 5929: with the
	// certificate's own signature hash, or SHA-256 when that is MD5 or SHA-1.
	if certificate == nil || len(certificate.Certificate) == 0 {
		return nil
	}
	leaf := certificate.Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			return nil
		}
		leaf = parsed
	}
	hash := crypto.SHA256
	switch leaf.SignatureAlgorithm {
	case x509.SHA384WithRSA, x509.ECDSAWithSHA384, x509.SHA384WithRSAPSS:
		hash = crypto.SHA384
	case x509.SHA512WithRSA, x509.ECDSAWithSHA512, x509.SHA512WithRSAPSS:
		hash = crypto.SHA512
	}
	h := hash.New()
	h.Write(leaf.Raw)
	return h.Sum(nil)
}

func bindingTLSConfig(base *tls.Config, served **tls.Certificate) *tls.Config {
	config := base.Clone()
	config.NextProtos = []string{alpnPostgreSQL}
	// crypto/tls skips GetCertificate when static certificates can answer, so
	// the clone carries none and every choice is made, and seen, below.
	config.Certificates = nil
	config.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		certificate, err := selectCertificate(base, hello)
		if err == nil {
			*served = certificate
		}
		return certificate, err
	}
	return config
}

func selectCertificate(base *tls.Config, hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// mirrors crypto/tls's own choice among a config's certificates.
	if base.GetCertificate != nil {
		if certificate, err := base.GetCertificate(hello); certificate != nil || err != nil {
			return certificate, err
		}
	}
	if len(base.Certificates) == 0 {
		return nil, errors.New("no TLS certificate configured")
	}
	for i := range base.Certificates {
		if hello.SupportsCertificate(&base.Certificates[i]) == nil {
			return &base.Certificates[i], nil
		}
	}
	return &base.Certificates[0], nil
}
