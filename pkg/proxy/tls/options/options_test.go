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
	"os"
	"path/filepath"
	"testing"
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
	o := New()
	err := o.UnmarshalYAML(func(v any) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
