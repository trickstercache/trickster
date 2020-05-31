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

package tls

import (
	"testing"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/proxy/tls/options"
)

func TestDefaultTLSConfig(t *testing.T) {

	dc := options.NewOptions()
	if dc == nil {
		t.Errorf("expected config named %s", "default")
	}

	if dc.FullChainCertPath != "" {
		t.Errorf("expected empty cert path got %s", dc.FullChainCertPath)
	}

	if dc.PrivateKeyPath != "" {
		t.Errorf("expected empty key path got %s", dc.PrivateKeyPath)
	}

}

func tlsConfig(id string) *options.Options {
	return &options.Options{
		FullChainCertPath: "../../../testdata/test." + id + ".cert.pem",
		PrivateKeyPath:    "../../../testdata/test." + id + ".key.pem",
		ServeTLS:          true,
	}
}

func TestVerifyTLSConfigs(t *testing.T) {

	tls01 := tlsConfig("01")

	_, err := tls01.Validate()
	if err != nil {
		t.Error(err)
	}

	// test for error when cert file can't be read
	tls04 := tlsConfig("04")
	originalFile := tls04.FullChainCertPath
	badFile := originalFile + ".nonexistent"
	tls04.FullChainCertPath = badFile

	_, err = tls04.Validate()
	if err == nil {
		t.Error("expected no such file or directory error")
	}

	tls04.FullChainCertPath = originalFile

	// test for error when key file can't be read
	originalFile = tls04.PrivateKeyPath
	badFile = originalFile + ".nonexistent"
	tls04.PrivateKeyPath = badFile
	_, err = tls04.Validate()
	if err == nil {
		t.Error("expected no such file or directory error")
	}

	tls04.PrivateKeyPath = originalFile
	originalFile = "../../../testdata/test.rootca.pem"
	badFile = originalFile + ".nonexistent"
	// test for more RootCA's to add
	tls04.CertificateAuthorityPaths = []string{originalFile}
	_, err = tls04.Validate()
	if err != nil {
		t.Error(err)
	}

	tls04.CertificateAuthorityPaths = []string{badFile}
	_, err = tls04.Validate()
	if err == nil {
		t.Error("expected no such file or directory error")
	}
}

func TestProcessTLSConfigs(t *testing.T) {

	a := []string{"-config", "../../../testdata/test.full.tls.conf"}
	_, _, err := config.Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

}

func TestTLSCertConfig(t *testing.T) {

	config := config.NewConfig()

	// test empty config condition #1 (ServeTLS is false, early bail)
	n, err := config.TLSCertConfig()
	if n != nil {
		t.Errorf("expected nil config, got %d certs", len(n.Certificates))
	}
	if err != nil {
		t.Error(err)
	}

	// test empty config condition 2 (ServeTLS is true, but there are 0 origins configured)
	config.Frontend.ServeTLS = true
	n, err = config.TLSCertConfig()
	if n != nil {
		t.Errorf("expected nil config, got %d certs", len(n.Certificates))
	}
	if err != nil {
		t.Error(err)
	}

	tls01 := tlsConfig("01")
	config.Frontend.ServeTLS = true

	// test good config
	config.Origins["default"].TLS = tls01
	_, err = config.TLSCertConfig()
	if err != nil {
		t.Error(err)
	}

	// test config with key file that has invalid key data
	expectedErr := "tls: failed to find any PEM data in key input"
	tls05 := tlsConfig("05")
	config.Origins["default"].TLS = tls05
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

	// test config with cert file that has invalid key data
	expectedErr = "tls: failed to find any PEM data in certificate input"
	tls06 := tlsConfig("06")
	config.Origins["default"].TLS = tls06
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

}

func TestOptionsChanged(t *testing.T) {

	c1 := config.NewConfig()
	c2 := config.NewConfig()

	c1.Origins["default"].TLS.ServeTLS = true
	c2.Origins["default"].TLS.ServeTLS = true

	b := OptionsChanged(nil, nil)
	if b {
		t.Errorf("expected false")
	}

	b = OptionsChanged(c1, nil)
	if !b {
		t.Errorf("expected true")
	}

	b = OptionsChanged(c1, c2)
	if b {
		t.Errorf("expected false")
	}

	c2.Origins["test"] = c2.Origins["default"].Clone()
	c2.Origins["test"].TLS.ClientCertPath = "test"

	b = OptionsChanged(c1, c2)
	if !b {
		t.Errorf("expected true")
	}

	delete(c2.Origins, "test")

	c1.Origins["test1"] = c1.Origins["default"].Clone()
	c1.Origins["test1"].TLS.ClientCertPath = "test1"

	b = OptionsChanged(c1, c2)
	if !b {
		t.Errorf("expected true")
	}

}
