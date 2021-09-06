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

package proxy

import (
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestNewHTTPClient(t *testing.T) {

	// test invalid backend options
	c, err := NewHTTPClient(nil)
	if c != nil {
		t.Errorf("expected nil client, got %v", c)
	}
	if err != nil {
		t.Error(err)
	}

	_, caFile, closer, err := tlstest.GetTestKeyAndCertFiles("ca")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Error(err)
	}

	caFileInvalid1 := caFile + ".invalid"
	const caFileInvalid2 = "../../testdata/test.06.cert.pem"

	// test good backend options, no CA
	o := bo.New()
	_, err = NewHTTPClient(o)
	if err != nil {
		t.Error(err)
	}

	// test good backend options, 1 good CA
	o.TLS.CertificateAuthorityPaths = []string{caFile}
	_, err = NewHTTPClient(o)
	if err != nil {
		t.Error(err)
	}

	// test good backend options, 1 bad CA (file not found)
	o.TLS.CertificateAuthorityPaths = []string{caFileInvalid1}
	_, err = NewHTTPClient(o)
	if err == nil {
		t.Errorf("expected error for no such file or directory on %s", caFileInvalid1)
	}

	// test good backend options, 1 bad CA (junk content)
	o.TLS.CertificateAuthorityPaths = []string{caFileInvalid2}
	_, err = NewHTTPClient(o)
	if err == nil {
		t.Errorf("expected error for unable to append to CA Certs from file %s", caFileInvalid2)
	}

	o.TLS.CertificateAuthorityPaths = []string{}

	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("")
	if err != nil {
		t.Error(err)
	}
	if closer != nil {
		defer closer()
	}

	o.TLS.ClientCertPath = cf
	o.TLS.ClientKeyPath = kf
	_, err = NewHTTPClient(o)
	if err != nil {
		t.Error(err)
	}

	o.TLS.ClientCertPath = "../../testdata/test.05.cert.pem"
	o.TLS.ClientKeyPath = "../../testdata/test.05.key.pem"
	o.TLS.CertificateAuthorityPaths = []string{}
	_, err = NewHTTPClient(o)
	if err == nil {
		t.Errorf("failed to find any PEM data in key input for file %s", o.TLS.ClientKeyPath)
	}
}
