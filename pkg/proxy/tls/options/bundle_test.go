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
	"encoding/pem"
	"errors"
	"os"
	"testing"

	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestValidateCABundle(t *testing.T) {
	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("ca")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(cf)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(kf)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := ValidateCABundle(certPEM); err != nil || n != 1 {
		t.Fatalf("expected one certificate, got %d %v", n, err)
	}
	// a bundle counts every certificate and ignores blocks of other types
	bundle := append(append(append([]byte{}, certPEM...), keyPEM...), certPEM...)
	if n, err := ValidateCABundle(bundle); err != nil || n != 2 {
		t.Fatalf("expected two certificates, got %d %v", n, err)
	}
	for name, in := range map[string][]byte{
		"empty":         nil,
		"not pem":       []byte("not a pem bundle"),
		"no certs":      keyPEM,
		"corrupt block": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}),
	} {
		if _, err := ValidateCABundle(in); !errors.Is(err, ErrInvalidCertificateAuthorityPEM) {
			t.Errorf("%s: expected %v, got %v", name, ErrInvalidCertificateAuthorityPEM, err)
		}
	}
}
