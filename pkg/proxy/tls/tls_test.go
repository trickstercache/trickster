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

package tls

import (
	"os"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestDefaultTLSConfig(t *testing.T) {

	dc := options.New()
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

func TestVerifyTLSConfigs(t *testing.T) {

	tls01, closer, err := tlsConfig("")
	if err != nil {
		t.Error(err)
	}
	if closer != nil {
		defer closer()
	}

	_, err = tls01.Validate()
	if err != nil {
		t.Error(err)
	}

	// test for error when cert file can't be read
	tls04, closer2, err2 := tlsConfig("")
	if err2 != nil {
		t.Error(err2)
	}
	if closer2 != nil {
		defer closer2()
	}
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

	_, caFile, closer, err := tlstest.GetTestKeyAndCertFiles("ca")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Error(err)
	}

	tls04.PrivateKeyPath = originalFile
	originalFile = caFile
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

	td := t.TempDir()
	confFile := td + "/trickster_test.conf"

	_, ca, _ := tlstest.GetTestKeyAndCert(true)
	caFile := td + "/rootca.01.pem"
	err := os.WriteFile(caFile, ca, 0600)
	if err != nil {
		t.Error(err)
	}

	k, c, _ := tlstest.GetTestKeyAndCert(false)

	certFile := td + "/01.cert.pem"
	err = os.WriteFile(certFile, c, 0600)
	if err != nil {
		t.Error(err)
	}

	keyfile := td + "/01.key.pem"
	err = os.WriteFile(keyfile, k, 0600)
	if err != nil {
		t.Error(err)
	}

	b, err := os.ReadFile("../../../testdata/test.full.tls.conf")
	b = []byte(strings.ReplaceAll(string(b), "../../../testdata/test.", td+"/"))

	err = os.WriteFile(confFile, b, 0600)
	if err != nil {
		t.Error(err)
	}

	a := []string{"-config", confFile}
	_, _, err = config.Load("trickster-test", "0", a)
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

	// test empty config condition 2 (ServeTLS is true, but there are 0 backends configured)
	config.Frontend.ServeTLS = true
	n, err = config.TLSCertConfig()
	if n != nil {
		t.Errorf("expected nil config, got %d certs", len(n.Certificates))
	}
	if err != nil {
		t.Error(err)
	}

	tls01, closer, err := tlsConfig("")
	if closer != nil {
		defer closer()
	}
	if err != nil {
		t.Error(err)
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
	tls05, closer5, err5 := tlsConfig("invalid-key")
	if closer5 != nil {
		defer closer5()
	}
	if err5 != nil {
		t.Error(err5)
	}
	config.Backends["default"].TLS = tls05
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

	// test config with cert file that has invalid key data
	expectedErr = "tls: failed to find any PEM data in certificate input"
	tls06, closer6, err6 := tlsConfig("invalid-cert")
	if closer6 != nil {
		defer closer6()
	}
	if err6 != nil {
		t.Error(err6)
	}
	config.Backends["default"].TLS = tls06
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

}

func TestOptionsChanged(t *testing.T) {

	c1 := config.NewConfig()
	c2 := config.NewConfig()

	c1.Backends["default"].TLS.ServeTLS = true
	c2.Backends["default"].TLS.ServeTLS = true

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

	c2.Backends["test"] = c2.Backends["default"].Clone()
	c2.Backends["test"].TLS.ClientCertPath = "test"

	b = OptionsChanged(c1, c2)
	if !b {
		t.Errorf("expected true")
	}

	delete(c2.Backends, "test")

	c1.Backends["test1"] = c1.Backends["default"].Clone()
	c1.Backends["test1"].TLS.ClientCertPath = "test1"

	b = OptionsChanged(c1, c2)
	if !b {
		t.Errorf("expected true")
	}

}
