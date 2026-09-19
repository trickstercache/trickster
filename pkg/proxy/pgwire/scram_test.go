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
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strconv"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/cred"
)

const (
	scramTestPassword    = "pencil"
	scramTestClientNonce = "rOprNGfwEbeRWgbNEkqO"
	scramTestSalt        = "0123456789abcdef"
	scramPlusHeader      = gs2BindingPrefix + bindingTLSServerEndPoint + ",,"
)

var scramTestBinding = []byte("server-certificate-hash")

func scramTestServer(t *testing.T, binding []byte) *scramServer {
	t.Helper()
	verifier, err := cred.NewSCRAMVerifier(scramTestPassword, []byte(scramTestSalt), cred.DefaultSCRAMIterations)
	if err != nil {
		t.Fatal(err)
	}
	return &scramServer{verifier: verifier, known: true, binding: binding}
}

func scramClientFinal(t *testing.T, password, gs2Header string, binding []byte, serverFirst string) (string, []byte) {
	t.Helper()
	salt, err := base64.StdEncoding.DecodeString(scramAttribute(serverFirst, 's'))
	if err != nil {
		t.Fatal(err)
	}
	iterations, err := strconv.Atoi(scramAttribute(serverFirst, 'i'))
	if err != nil {
		t.Fatal(err)
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	withoutProof := "c=" + base64.StdEncoding.EncodeToString(append([]byte(gs2Header), binding...)) +
		",r=" + scramAttribute(serverFirst, 'r')
	authMessage := []byte("n=,r=" + scramTestClientNonce + "," + serverFirst + "," + withoutProof)
	proof := make([]byte, sha256.Size)
	subtle.XORBytes(proof, clientKey, hmacSHA256(storedKey[:], authMessage))
	expectedServer := hmacSHA256(hmacSHA256(salted, []byte("Server Key")), authMessage)
	return withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof), expectedServer
}

func TestSCRAMExchange(t *testing.T) {
	for name, test := range map[string]struct {
		mechanism string
		header    string
		binding   []byte
	}{
		"plain":           {mechSCRAMSHA256, gs2NoBinding, nil},
		"client no bind":  {mechSCRAMSHA256, gs2ClientOnlyNoBind, nil},
		"channel binding": {mechSCRAMSHA256Plus, scramPlusHeader, scramTestBinding},
		"plain over TLS":  {mechSCRAMSHA256, gs2NoBinding, scramTestBinding},
	} {
		t.Run(name, func(t *testing.T) {
			server := scramTestServer(t, test.binding)
			wantMechanisms := 1
			if test.binding != nil {
				wantMechanisms = 2
			}
			if got := server.mechanisms(); len(got) != wantMechanisms || got[len(got)-1] != mechSCRAMSHA256 {
				t.Fatalf("unexpected mechanisms %v", got)
			}
			serverFirst, err := server.first(test.mechanism, []byte(test.header+"n=,r="+scramTestClientNonce))
			if err != nil {
				t.Fatal(err)
			}
			var bound []byte
			if test.mechanism == mechSCRAMSHA256Plus {
				bound = test.binding
			}
			clientFinal, expected := scramClientFinal(t, scramTestPassword, test.header, bound, string(serverFirst))
			serverFinal, err := server.final([]byte(clientFinal))
			if err != nil {
				t.Fatal(err)
			}
			if want := "v=" + base64.StdEncoding.EncodeToString(expected); string(serverFinal) != want {
				t.Fatalf("server signature %q, want %q", serverFinal, want)
			}
		})
	}
}

func TestSCRAMFirstRejections(t *testing.T) {
	bare := "n=,r=" + scramTestClientNonce
	for name, test := range map[string]struct {
		mechanism string
		message   string
		binding   []byte
	}{
		"unknown mechanism":        {"PLAIN", gs2NoBinding + bare, nil},
		"plus without TLS":         {mechSCRAMSHA256Plus, scramPlusHeader + bare, nil},
		"stripped binding":         {mechSCRAMSHA256, gs2ClientOnlyNoBind + bare, scramTestBinding},
		"plus without gs2 binding": {mechSCRAMSHA256Plus, gs2NoBinding + bare, scramTestBinding},
		"gs2 binding without plus": {mechSCRAMSHA256, scramPlusHeader + bare, scramTestBinding},
		"unsupported binding type": {mechSCRAMSHA256Plus, gs2BindingPrefix + "tls-unique,," + bare, scramTestBinding},
		"binding without end":      {mechSCRAMSHA256Plus, gs2BindingPrefix + bindingTLSServerEndPoint, scramTestBinding},
		"no gs2 header":            {mechSCRAMSHA256, bare, nil},
		"no nonce":                 {mechSCRAMSHA256, gs2NoBinding + "n=", nil},
	} {
		if _, err := scramTestServer(t, test.binding).first(test.mechanism, []byte(test.message)); !errors.Is(err, errSCRAM) {
			t.Fatalf("%s: expected errSCRAM, got %v", name, err)
		}
	}
}

func TestSCRAMFinalRejections(t *testing.T) {
	begin := func(t *testing.T, server *scramServer) string {
		t.Helper()
		serverFirst, err := server.first(mechSCRAMSHA256, []byte(gs2NoBinding+"n=,r="+scramTestClientNonce))
		if err != nil {
			t.Fatal(err)
		}
		return string(serverFirst)
	}
	server := scramTestServer(t, nil)
	serverFirst := begin(t, server)
	valid, _ := scramClientFinal(t, scramTestPassword, gs2NoBinding, nil, serverFirst)
	wrongPassword, _ := scramClientFinal(t, scramTestPassword+"x", gs2NoBinding, nil, serverFirst)
	wrongBinding, _ := scramClientFinal(t, scramTestPassword, gs2ClientOnlyNoBind, nil, serverFirst)
	for name, message := range map[string]string{
		"no proof":       "c=biws,r=" + scramAttribute(serverFirst, 'r'),
		"proof not b64":  "c=biws,r=x,p=!!!",
		"short proof":    "c=biws,r=x,p=" + base64.StdEncoding.EncodeToString([]byte("short")),
		"wrong password": wrongPassword,
		"wrong binding":  wrongBinding,
		"wrong nonce":    "c=biws,r=someone-elses-nonce" + valid[bytes.LastIndexByte([]byte(valid), ','):],
	} {
		if _, err := server.final([]byte(message)); !errors.Is(err, errSCRAM) {
			t.Fatalf("%s: expected errSCRAM, got %v", name, err)
		}
	}
	// a mock exchange for an unknown user fails even with a well-formed proof
	mock := &scramServer{verifier: mockVerifier([]byte("server-secret"), "nobody")}
	mockFinal, _ := scramClientFinal(t, scramTestPassword, gs2NoBinding, nil, begin(t, mock))
	if _, err := mock.final([]byte(mockFinal)); !errors.Is(err, errSCRAM) {
		t.Fatalf("expected the mock exchange to fail, got %v", err)
	}
}

func TestMockVerifierIsStablePerUser(t *testing.T) {
	secret := []byte("server-secret")
	first, again, other := mockVerifier(secret, "nobody"), mockVerifier(secret, "nobody"), mockVerifier(secret, "somebody")
	if !bytes.Equal(first.Salt, again.Salt) || bytes.Equal(first.Salt, other.Salt) || len(first.Salt) != cred.SCRAMSaltLen {
		t.Fatal("the mock salt must be stable per user and differ between users")
	}
}

func TestTLSServerEndPoint(t *testing.T) {
	config := testServerTLS(t)
	certificate := &config.Certificates[0]
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(leaf.Raw)
	if got := tlsServerEndPoint(certificate); !bytes.Equal(got, want[:]) {
		t.Fatal("an ECDSA-SHA256 certificate must be hashed with SHA-256")
	}
	certificate.Leaf = leaf
	if got := tlsServerEndPoint(certificate); !bytes.Equal(got, want[:]) {
		t.Fatal("a pre-parsed leaf must hash identically")
	}
	for algorithm, size := range map[x509.SignatureAlgorithm]int{
		x509.SHA1WithRSA: sha256.Size, x509.ECDSAWithSHA384: 48, x509.SHA512WithRSA: 64,
	} {
		copied := *leaf
		copied.SignatureAlgorithm = algorithm
		if got := tlsServerEndPoint(&tls.Certificate{Certificate: certificate.Certificate, Leaf: &copied}); len(got) != size {
			t.Fatalf("%v: expected a %d-byte hash, got %d", algorithm, size, len(got))
		}
	}
	if tlsServerEndPoint(nil) != nil || tlsServerEndPoint(&tls.Certificate{}) != nil ||
		tlsServerEndPoint(&tls.Certificate{Certificate: [][]byte{[]byte("not DER")}}) != nil {
		t.Fatal("an unusable certificate must produce no binding")
	}
}

func TestSelectCertificate(t *testing.T) {
	static := testServerTLS(t)
	hello := &tls.ClientHelloInfo{
		CipherSuites: []uint16{tls.TLS_AES_128_GCM_SHA256}, SupportedVersions: []uint16{tls.VersionTLS13},
		SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256},
	}
	if got, err := selectCertificate(static, hello); err != nil || got != &static.Certificates[0] {
		t.Fatalf("expected the static certificate, got %v, %v", got, err)
	}
	// a hello no certificate supports still gets the first one, as crypto/tls does
	if got, err := selectCertificate(static, &tls.ClientHelloInfo{}); err != nil || got != &static.Certificates[0] {
		t.Fatalf("expected the fallback certificate, got %v, %v", got, err)
	}
	dynamic := &tls.Config{GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return &static.Certificates[0], nil
	}}
	if got, err := selectCertificate(dynamic, hello); err != nil || got != &static.Certificates[0] {
		t.Fatalf("expected the dynamic certificate, got %v, %v", got, err)
	}
	if _, err := selectCertificate(&tls.Config{}, hello); err == nil {
		t.Fatal("expected a config with no certificate to fail")
	}
	var served *tls.Certificate
	recorded, err := bindingTLSConfig(static, &served).GetCertificate(hello)
	if err != nil || recorded != served || served == nil {
		t.Fatalf("expected the served certificate to be recorded, got %v, %v", served, err)
	}
	if _, err = bindingTLSConfig(&tls.Config{}, &served).GetCertificate(hello); err == nil {
		t.Fatal("expected a selection failure to propagate")
	}
}
