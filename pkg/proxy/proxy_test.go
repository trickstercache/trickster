/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	oo "github.com/tricksterproxy/trickster/pkg/proxy/origins/options"
	tlstest "github.com/tricksterproxy/trickster/pkg/util/testing/tls"
)

func TestNewHTTPClient(t *testing.T) {

	// test invalid origin config
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

	// test good originconfig, no CA
	oc := oo.NewOptions()
	_, err = NewHTTPClient(oc)
	if err != nil {
		t.Error(err)
	}

	// test good originconfig, 1 good CA
	oc.TLS.CertificateAuthorityPaths = []string{caFile}
	_, err = NewHTTPClient(oc)
	if err != nil {
		t.Error(err)
	}

	// test good originconfig, 1 bad CA (file not found)
	oc.TLS.CertificateAuthorityPaths = []string{caFileInvalid1}
	_, err = NewHTTPClient(oc)
	if err == nil {
		t.Errorf("expected error for no such file or directory on %s", caFileInvalid1)
	}

	// test good originconfig, 1 bad CA (junk content)
	oc.TLS.CertificateAuthorityPaths = []string{caFileInvalid2}
	_, err = NewHTTPClient(oc)
	if err == nil {
		t.Errorf("expected error for unable to append to CA Certs from file %s", caFileInvalid2)
	}

	oc.TLS.CertificateAuthorityPaths = []string{}

	kf, cf, closer, err := tlstest.GetTestKeyAndCertFiles("")
	if err != nil {
		t.Error(err)
	}
	if closer != nil {
		defer closer()
	}

	oc.TLS.ClientCertPath = cf
	oc.TLS.ClientKeyPath = kf
	_, err = NewHTTPClient(oc)
	if err != nil {
		t.Error(err)
	}

	oc.TLS.ClientCertPath = "../../testdata/test.05.cert.pem"
	oc.TLS.ClientKeyPath = "../../testdata/test.05.key.pem"
	oc.TLS.CertificateAuthorityPaths = []string{}
	_, err = NewHTTPClient(oc)
	if err == nil {
		t.Errorf("failed to find any PEM data in key input for file %s", oc.TLS.ClientKeyPath)
	}
}
