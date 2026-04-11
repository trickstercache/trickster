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

package config

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestTLSCertConfig(t *testing.T) {
	config := NewConfig()

	// test empty config condition #1 (ServeTLS is false, early bail)
	n, err := config.TLSCertConfig()
	if n != nil {
		t.Errorf("expected nil config, got %d certs", len(n.Certificates))
	}
	if err != nil {
		t.Error(err)
	}

	// test empty config condition 2 (ServeTLS is true, but there are 0 backends configured)
	config.Frontend.ServeTLS = true
	n, err = config.TLSCertConfig()
	if n != nil {
		t.Errorf("expected nil config, got %d certs", len(n.Certificates))
	}
	if err != nil {
		t.Error(err)
	}

	tls01, closer01, err01 := tlsConfig("")
	if closer01 != nil {
		defer closer01()
	}
	if err01 != nil {
		t.Error(err01)
	}
	config.Frontend.ServeTLS = true

	// test good config
	config.Backends["default"].TLS = tls01
	_, err = config.TLSCertConfig()
	if err != nil {
		t.Error(err)
	}

	// test config with key file that has invalid key data
	expectedErr := "tls: failed to find any PEM data in key input"
	tls05, closer05, err05 := tlsConfig("invalid-key")
	if closer05 != nil {
		defer closer05()
	}
	if err05 != nil {
		t.Error(err05)
	}
	config.Backends["default"].TLS = tls05
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

	// test config with cert file that has invalid cert data
	expectedErr = "tls: failed to find any PEM data in certificate input"
	tls06, closer06, err06 := tlsConfig("invalid-cert")
	if closer06 != nil {
		defer closer06()
	}
	if err06 != nil {
		t.Error(err06)
	}
	config.Backends["default"].TLS = tls06
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}
}

func tlsConfig(condition string) (*options.Options, func(), error) {
	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles(condition)
	if err != nil {
		return nil, nil, err
	}

	return &options.Options{
		FullChainCertPath: cf,
		PrivateKeyPath:    kf,
		ServeTLS:          true,
	}, closer, nil
}

// TestTLSCertConfig_CAOnlyBackendExcluded locks in the #940 guard in
// TLSCertConfig: a backend whose TLS block has ServeTLS=true (e.g. set by
// an older config load or test harness) but no server cert/key paths must
// NOT be fed to tls.LoadX509KeyPair("", "") — it must be silently excluded.
// This is the unit-level analogue of the listeners.go reload-failure path:
// a misconfigured backend cannot crash the whole TLS setup func.
func TestTLSCertConfig_CAOnlyBackendExcluded(t *testing.T) {
	config := NewConfig()
	config.Frontend.ServeTLS = true

	// CA-only: ServeTLS flipped true but no cert+key paths. Pre-#940 this
	// would trip LoadX509KeyPair("","") with "open : no such file".
	config.Backends["default"].TLS = &options.Options{
		CertificateAuthorityPaths: []string{"/some/ca.pem"},
		ServeTLS:                  true,
	}
	got, err := config.TLSCertConfig()
	if err != nil {
		t.Errorf("CA-only backend must not produce an error; got %v", err)
	}
	if got != nil {
		t.Errorf("CA-only backend must contribute 0 certs; got %d", len(got.Certificates))
	}
}

// TestTLSCertConfig_MixedBackendsOnlyValidContribute verifies that a mix of
// a CA-only backend and a valid cert+key backend yields a tls.Config with
// exactly the valid backend's certificate — the CA-only one is skipped
// without error.
func TestTLSCertConfig_MixedBackendsOnlyValidContribute(t *testing.T) {
	config := NewConfig()
	config.Frontend.ServeTLS = true

	validTLS, closer, err := tlsConfig("")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}

	// Reuse the "default" backend for the valid cert+key, add a second
	// backend with only a CA path.
	config.Backends["default"].TLS = validTLS
	config.Backends["ca-only"] = config.Backends["default"].Clone()
	config.Backends["ca-only"].TLS = &options.Options{
		CertificateAuthorityPaths: []string{"/some/ca.pem"},
		ServeTLS:                  true,
	}

	got, err := config.TLSCertConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if len(got.Certificates) != 1 {
		t.Errorf("expected exactly 1 certificate (only the valid backend contributes), got %d",
			len(got.Certificates))
	}
}
