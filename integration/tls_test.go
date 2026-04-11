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

package integration

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTLS_CAOnlyConfig starts Trickster with a backend TLS block that
// contains only a certificate_authority_paths entry (no full chain cert,
// no private key, no client cert/key). Prior to #940 this panicked because
// tls/options Initialize flipped ServeTLS=true for CA-only configs, which
// cascaded into LoadX509KeyPair("","") both in config.TLSCertConfig and
// in daemon/setup/listeners.go's reload path.
//
// regression: #940
func TestTLS_CAOnlyConfig(t *testing.T) {
	// Generate a self-signed CA PEM on the fly and write it to the location
	// referenced by testdata/configs/tls.yaml. Using a runtime-generated PEM
	// keeps the testdata tree free of crypto blobs that are hard to audit.
	caPath := "testdata/configs/ca.pem"
	writeSelfSignedCA(t, caPath)
	t.Cleanup(func() { _ = os.Remove(caPath) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Any panic inside daemon.Start's goroutine would crash the test binary;
	// surviving past waitForTrickster on the metrics listener is proof that
	// the CA-only TLS path no longer panics.
	go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/configs/tls.yaml")

	const metricsAddr = "127.0.0.1:8534"
	waitForTrickster(t, metricsAddr)
}

// writeSelfSignedCA generates a minimal self-signed CA certificate and
// writes the PEM to path. The CA is only used to satisfy the ReadFile +
// AppendCertsFromPEM code path in pkg/proxy/proxy.go; it is never presented
// to any network peer during the test.
func writeSelfSignedCA(t *testing.T, path string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "trickster-integration-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
