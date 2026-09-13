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

package options

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"go.yaml.in/yaml/v3"
)

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNew(t *testing.T) {
	o := New()
	if o.FullChainCertPath != "" {
		t.Errorf("expected empty FullChainCertPath, got %q", o.FullChainCertPath)
	}
	if o.PrivateKeyPath != "" {
		t.Errorf("expected empty PrivateKeyPath, got %q", o.PrivateKeyPath)
	}
}

func TestClone(t *testing.T) {
	o := &Options{
		FullChainCertPath:         "/cert",
		PrivateKeyPath:            "/key",
		InsecureSkipVerify:        true,
		CertificateAuthorityPaths: []string{"/ca1", "/ca2"},
		ClientCertPath:            "/client.crt",
		ClientKeyPath:             "/client.key",
	}
	c := o.Clone()
	if !o.Equal(c) {
		t.Error("clone should be equal to original")
	}
	c.CertificateAuthorityPaths[0] = "/changed"
	if o.CertificateAuthorityPaths[0] == "/changed" {
		t.Error("clone should not share slice backing array")
	}
}

func TestEqual(t *testing.T) {
	o1 := &Options{ClientCertPath: "/a"}
	o2 := &Options{ClientCertPath: "/a"}
	o3 := &Options{ClientCertPath: "/b"}

	if !o1.Equal(o2) {
		t.Error("identical options should be equal")
	}
	if o1.Equal(o3) {
		t.Error("different options should not be equal")
	}
}

func TestInitialize(t *testing.T) {
	// CA-only: ServeTLS must stay false — a CA bundle only governs
	// outbound peer verification and must not flip the frontend into
	// TLS-serving mode. See #940.
	caOnly := &Options{CertificateAuthorityPaths: []string{"/ca"}}
	if err := caOnly.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if caOnly.ServeTLS {
		t.Error("CA-only options must NOT flip ServeTLS=true (#940)")
	}

	// Empty options: ServeTLS stays false.
	empty := New()
	if err := empty.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if empty.ServeTLS {
		t.Error("expected ServeTLS=false for empty options")
	}

	// Full server cert+key pair: ServeTLS flips true.
	pair := &Options{FullChainCertPath: "/cert", PrivateKeyPath: "/key"}
	if err := pair.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if !pair.ServeTLS {
		t.Error("expected ServeTLS=true when cert+key pair is configured")
	}

	// Cert+key AND CA: still ServeTLS=true (the CA adds mTLS verification).
	pairWithCA := &Options{
		FullChainCertPath:         "/cert",
		PrivateKeyPath:            "/key",
		CertificateAuthorityPaths: []string{"/ca"},
	}
	if err := pairWithCA.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if !pairWithCA.ServeTLS {
		t.Error("expected ServeTLS=true for cert+key+CA")
	}

	// Cert without key (malformed): must not flip ServeTLS.
	certOnly := &Options{FullChainCertPath: "/cert"}
	if err := certOnly.Initialize(""); err != nil {
		t.Fatal(err)
	}
	if certOnly.ServeTLS {
		t.Error("cert without key must NOT flip ServeTLS")
	}
}

func TestUnmarshalYAML(t *testing.T) {
	o := &Options{}
	if err := yaml.Unmarshal([]byte("full_chain_cert_path: /cert"), o); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if o.FullChainCertPath != "/cert" {
		t.Errorf("expected FullChainCertPath /cert, got %q", o.FullChainCertPath)
	}

	// an empty mapping retains the defaults applied by UnmarshalYAML
	o2 := &Options{}
	if err := yaml.Unmarshal([]byte("{}"), o2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !o2.Equal(New()) {
		t.Errorf("expected defaults, got %+v", o2)
	}

	// a sequence cannot be decoded into the options struct
	if err := yaml.Unmarshal([]byte("- boom"), &Options{}); err == nil {
		t.Error("expected an error")
	}
}

func TestValidate_NoTLS(t *testing.T) {
	o := New()
	ok, err := o.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false for empty TLS options")
	}
}

func TestValidate_FullServerTLS(t *testing.T) {
	td := t.TempDir()
	cert := writeTempFile(t, td, "cert.pem", "CERT")
	key := writeTempFile(t, td, "key.pem", "KEY")

	o := &Options{
		FullChainCertPath: cert,
		PrivateKeyPath:    key,
	}
	ok, err := o.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected true for valid server cert+key")
	}
}

func TestValidate_ServerTLS_BadCertPath(t *testing.T) {
	td := t.TempDir()
	key := writeTempFile(t, td, "key.pem", "KEY")

	o := &Options{
		FullChainCertPath: "/nonexistent/cert.pem",
		PrivateKeyPath:    key,
	}
	_, err := o.Validate()
	if err == nil {
		t.Error("expected error for missing cert file")
	}
}

func TestValidate_ServerTLS_BadKeyPath(t *testing.T) {
	td := t.TempDir()
	cert := writeTempFile(t, td, "cert.pem", "CERT")

	o := &Options{
		FullChainCertPath: cert,
		PrivateKeyPath:    "/nonexistent/key.pem",
	}
	_, err := o.Validate()
	if err == nil {
		t.Error("expected error for missing key file")
	}
}

func TestValidate_CAPathsOnly(t *testing.T) {
	td := t.TempDir()
	ca := writeTempFile(t, td, "ca.pem", "CA")

	o := &Options{
		CertificateAuthorityPaths: []string{ca},
	}
	ok, err := o.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected true for valid CA-only config")
	}
}

func TestValidate_CAPathsOnly_BadPath(t *testing.T) {
	o := &Options{
		CertificateAuthorityPaths: []string{"/nonexistent/ca.pem"},
	}
	_, err := o.Validate()
	if err == nil {
		t.Error("expected error for missing CA file")
	}
}

func TestValidate_ClientCertsWithCAPaths(t *testing.T) {
	td := t.TempDir()
	ca := writeTempFile(t, td, "ca.pem", "CA")
	clientCert := writeTempFile(t, td, "client.crt", "CLIENT_CERT")
	clientKey := writeTempFile(t, td, "client.key", "CLIENT_KEY")

	o := &Options{
		CertificateAuthorityPaths: []string{ca},
		ClientCertPath:            clientCert,
		ClientKeyPath:             clientKey,
	}
	ok, err := o.Validate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected true for valid mTLS client config")
	}
}

func TestValidate_ClientCertBadPath(t *testing.T) {
	td := t.TempDir()
	ca := writeTempFile(t, td, "ca.pem", "CA")

	o := &Options{
		CertificateAuthorityPaths: []string{ca},
		ClientCertPath:            "/nonexistent/client.crt",
	}
	_, err := o.Validate()
	if err == nil {
		t.Error("expected error for missing client cert file")
	}
}

func TestValidate_ClientKeyBadPath(t *testing.T) {
	td := t.TempDir()
	ca := writeTempFile(t, td, "ca.pem", "CA")
	clientCert := writeTempFile(t, td, "client.crt", "CLIENT_CERT")

	o := &Options{
		CertificateAuthorityPaths: []string{ca},
		ClientCertPath:            clientCert,
		ClientKeyPath:             "/nonexistent/client.key",
	}
	_, err := o.Validate()
	if err == nil {
		t.Error("expected error for missing client key file")
	}
}

// ToClientTLSConfig is the single implementation of outbound client TLS,
// shared by the proxy, the discovery pollers and the health checker; these
// cover each branch directly rather than only through pkg/proxy.

func TestToClientTLSConfigNilReceiver(t *testing.T) {
	var o *Options
	c, err := o.ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c != nil {
		t.Error("a nil Options should yield a nil config so callers can pass an absent TLS block through")
	}
}

func TestToClientTLSConfigInsecureSkipVerify(t *testing.T) {
	c, err := (&Options{InsecureSkipVerify: true}).ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !c.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to carry through")
	}
	if len(c.Certificates) != 0 || c.RootCAs != nil {
		t.Error("expected no client cert or CA pool when neither is configured")
	}
}

func TestToClientTLSConfigClientCertificate(t *testing.T) {
	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := (&Options{ClientCertPath: cf, ClientKeyPath: kf}).ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Certificates) != 1 {
		t.Errorf("expected 1 client certificate, got %d", len(c.Certificates))
	}
}

// A cert without its key (or vice versa) is not a partial success: the pair
// is simply not loaded, matching the behavior pkg/proxy had before this
// moved here.
func TestToClientTLSConfigIgnoresHalfAPair(t *testing.T) {
	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]*Options{
		"cert only": {ClientCertPath: cf},
		"key only":  {ClientKeyPath: kf},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := o.ToClientTLSConfig()
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Certificates) != 0 {
				t.Error("expected no certificate loaded from half a pair")
			}
		})
	}
}

func TestToClientTLSConfigBadClientCertificate(t *testing.T) {
	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("invalid-cert")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Options{ClientCertPath: cf, ClientKeyPath: kf}).ToClientTLSConfig(); err == nil {
		t.Error("expected an error loading an invalid client certificate")
	}
}

// Configured CAs are additive to the system pool, so a config that names a
// CA still trusts public roots.
func TestToClientTLSConfigCertificateAuthoritiesAreAdditive(t *testing.T) {
	_, cf, closer, err := tlstest.GetTestKeyAndCertFiles("ca")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := (&Options{CertificateAuthorityPaths: []string{cf}}).ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.RootCAs == nil {
		t.Fatal("expected a root CA pool")
	}
	system, _ := x509.SystemCertPool()
	if system != nil && len(c.RootCAs.Subjects()) <= len(system.Subjects()) { //nolint:staticcheck // Subjects is adequate for a count comparison in tests
		t.Error("expected the configured CA to be added to the system pool, not to replace it")
	}
}

func TestToClientTLSConfigMissingCAFile(t *testing.T) {
	o := &Options{CertificateAuthorityPaths: []string{"/nonexistent/ca.pem"}}
	if _, err := o.ToClientTLSConfig(); err == nil {
		t.Error("expected an error for an unreadable CA file")
	}
}

func TestToClientTLSConfigUnparsableCAFile(t *testing.T) {
	dir := t.TempDir()
	bad := writeTempFile(t, dir, "ca.pem", "not a pem file")
	o := &Options{CertificateAuthorityPaths: []string{bad}}
	if _, err := o.ToClientTLSConfig(); err == nil {
		t.Error("expected an error for a CA file containing no certificates")
	}
}

// The inline CA bundle and server name are client-side settings that travel
// as configuration rather than as files, so they must clone, compare and
// render into the client config like the file-based ones do
func TestInlineCertificateAuthorityAndServerName(t *testing.T) {
	_, cf, closer, err := tlstest.GetTestKeyAndCertFiles("ca")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}
	pem, err := os.ReadFile(cf)
	if err != nil {
		t.Fatal(err)
	}
	o := &Options{CertificateAuthorityPEM: string(pem), ServerName: "origin.example.com"}
	if !o.Equal(o.Clone()) {
		t.Error("clone should carry the inline CA and server name")
	}
	if o.Equal(&Options{CertificateAuthorityPEM: string(pem)}) {
		t.Error("a different server name must not compare equal")
	}
	if o.Equal(&Options{ServerName: "origin.example.com"}) {
		t.Error("a different inline CA must not compare equal")
	}
	ok, err := o.Validate()
	if err != nil || !ok {
		t.Fatalf("expected inline client settings to validate, got %t %v", ok, err)
	}
	ok, err = (&Options{ServerName: "origin.example.com"}).Validate()
	if err != nil || !ok {
		t.Fatalf("expected a server name alone to validate, got %t %v", ok, err)
	}
	c, err := o.ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerName != "origin.example.com" {
		t.Errorf("expected server name to reach the client config, got %q", c.ServerName)
	}
	if c.RootCAs == nil {
		t.Fatal("expected the inline CA to populate a root pool")
	}
	system, _ := x509.SystemCertPool()
	if system != nil && len(c.RootCAs.Subjects()) <= len(system.Subjects()) { //nolint:staticcheck // Subjects is adequate for a count comparison in tests
		t.Error("expected the inline CA to be added to the system pool")
	}
}

func TestInlineCertificateAuthorityRejectsGarbage(t *testing.T) {
	o := &Options{CertificateAuthorityPEM: "not a pem bundle"}
	if _, err := o.Validate(); !errors.Is(err, ErrInvalidCertificateAuthorityPEM) {
		t.Errorf("expected %v from Validate, got %v", ErrInvalidCertificateAuthorityPEM, err)
	}
	if _, err := o.ToClientTLSConfig(); !errors.Is(err, ErrInvalidCertificateAuthorityPEM) {
		t.Errorf("expected %v from ToClientTLSConfig, got %v",
			ErrInvalidCertificateAuthorityPEM, err)
	}
}

// testCA is a certificate authority and the PEM it is trusted by
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{
		cert: cert, key: key,
		pem: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// leaf issues a server certificate for hostname signed by the CA
func (ca testCA) leaf(t *testing.T, hostname string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// serveWith starts a TLS server presenting the certificate and returns its URL
func serveWith(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL
}

// get connects with the client configuration and returns the error, if any
func get(t *testing.T, o *Options, url string) error {
	t.Helper()
	cfg, err := o.ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// An exclusive trust pool trusts the configured authorities and nothing
// else: an origin certificate from any other authority, well known or not,
// is refused. The additive pool still holds the configured authorities, so
// the exclusive one is a subset of it rather than a different set.
func TestExcludeSystemRootsTrustsOnlyConfiguredCAs(t *testing.T) {
	const hostname = "backend.test"
	caA, caB := newTestCA(t, "CA A"), newTestCA(t, "CA B")
	urlA, urlB := serveWith(t, caA.leaf(t, hostname)), serveWith(t, caB.leaf(t, hostname))

	exclusive := &Options{
		CertificateAuthorityPEM: caA.pem, ServerName: hostname,
		ExcludeSystemRoots: true,
	}
	if err := get(t, exclusive, urlA); err != nil {
		t.Fatalf("a certificate from the configured CA must be accepted: %v", err)
	}
	err := get(t, exclusive, urlB)
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); !ok {
		t.Fatalf("a certificate from any other CA must be refused, got %v", err)
	}

	cfg, err := exclusive.ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cfg.RootCAs.Subjects()); n != 1 { //nolint:staticcheck // Subjects is adequate for a count in tests
		t.Fatalf("the exclusive pool must hold the configured CA alone, got %d subjects", n)
	}
	additive, err := (&Options{CertificateAuthorityPEM: caA.pem}).ToClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if system, _ := x509.SystemCertPool(); system != nil &&
		len(additive.RootCAs.Subjects()) <= len(system.Subjects()) { //nolint:staticcheck // Subjects is adequate for a count in tests
		t.Error("the additive pool must hold the configured CA on top of the system roots")
	}
	if !exclusive.Equal(exclusive.Clone()) {
		t.Error("the flag must survive a clone")
	}
	if exclusive.Equal(&Options{CertificateAuthorityPEM: caA.pem, ServerName: hostname}) {
		t.Error("exclusive and additive configurations must not compare equal")
	}
}

// Excluding the system roots with no authority configured would trust
// nothing and fail every handshake; it is refused as configuration
func TestExcludeSystemRootsRequiresAnAuthority(t *testing.T) {
	_, err := (&Options{ExcludeSystemRoots: true}).Validate()
	if !errors.Is(err, ErrExcludeSystemRootsWithoutCAs) {
		t.Fatalf("expected %v, got %v", ErrExcludeSystemRootsWithoutCAs, err)
	}
	ok, err := (&Options{ExcludeSystemRoots: true, CertificateAuthorityPaths: []string{"/x"}}).Validate()
	if ok || err == nil || errors.Is(err, ErrExcludeSystemRootsWithoutCAs) {
		t.Fatalf("a configured path satisfies the requirement and is then checked itself, got %t %v", ok, err)
	}
}
