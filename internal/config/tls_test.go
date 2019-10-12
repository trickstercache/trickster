/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"testing"
)

func TestDefaultTLSConfig(t *testing.T) {

	dc := DefaultTLSConfig()
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

func tlsMap() map[string]*TLSConfig {
	return map[string]*TLSConfig{
		"test.01": &TLSConfig{FullChainCertPath: "../../testdata/test.01.cert.pem", PrivateKeyPath: "../../testdata/test.01.key.pem"},
		"test.02": &TLSConfig{FullChainCertPath: "../../testdata/test.02.cert.pem", PrivateKeyPath: "../../testdata/test.02.key.pem"},
		"test.03": &TLSConfig{FullChainCertPath: "../../testdata/test.03.cert.pem", PrivateKeyPath: "../../testdata/test.03.key.pem"},
		"test.04": &TLSConfig{FullChainCertPath: "../../testdata/test.04.cert.pem", PrivateKeyPath: "../../testdata/test.04.key.pem"},
	}
}

func TestVerifyTLSConfigs(t *testing.T) {

	config := NewConfig()
	config.TLS = tlsMap()

	err := config.verifyTLSConfigs()
	if err != nil {
		t.Error(err)
	}

	// test for error when cert file can't be read
	originalFile := config.TLS["test.04"].FullChainCertPath
	badFile := originalFile + ".nonexistent"
	config.TLS["test.04"].FullChainCertPath = badFile
	err = config.verifyTLSConfigs()
	if err == nil {
		t.Errorf("expected error for bad file %s", badFile)
	}
	config.TLS["test.04"].FullChainCertPath = originalFile

	// test for error when key file can't be read
	originalFile = config.TLS["test.04"].PrivateKeyPath
	badFile = originalFile + ".nonexistent"
	config.TLS["test.04"].PrivateKeyPath = badFile
	err = config.verifyTLSConfigs()
	if err == nil {
		t.Errorf("expected error for bad file %s", badFile)
	}

}

func TestProcessTLSConfigs(t *testing.T) {

	a := []string{"-config", "../../testdata/test.full.conf"}
	// it should not error if config path is not set
	err := Load("trickster-test", "0", a)
	if err != nil {
		t.Error(err)
	}

}

func TestTLSCertConfig(t *testing.T) {
	config := NewConfig()

	// test empty config
	n, err := config.TLSCertConfig()
	if n != nil {
		t.Errorf("expected nil config, got %d certs", len(n.Certificates))
	}
	if err != nil {
		t.Error(err)
	}

	// test good config
	config.TLS = tlsMap()
	_, err = config.TLSCertConfig()
	if err != nil {
		t.Error(err)
	}

	// test config with key file that has invalid key data
	expectedErr := "tls: failed to find any PEM data in key input"
	config.TLS["test.05"] = &TLSConfig{FullChainCertPath: "../../testdata/test.05.cert.pem", PrivateKeyPath: "../../testdata/test.05.key.pem"}
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}
	delete(config.TLS, "test.05")

	// test config with cert file that has invalid key data
	expectedErr = "tls: failed to find any PEM data in certificate input"
	config.TLS["test.06"] = &TLSConfig{FullChainCertPath: "../../testdata/test.06.cert.pem", PrivateKeyPath: "../../testdata/test.06.key.pem"}
	_, err = config.TLSCertConfig()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

}

func TestTLSListener(t *testing.T) {
	config := NewConfig()
	config.TLS = tlsMap()
	config.ProxyServer = &ProxyServerConfig{TLSListenAddress: "", TLSListenPort: 0}
	_, err := config.TLSListener()
	if err != nil {
		t.Error(err)
	}

	// test config with key file that has invalid key data
	expectedErr := "tls: failed to find any PEM data in key input"
	config.TLS["test.05"] = &TLSConfig{FullChainCertPath: "../../testdata/test.05.cert.pem", PrivateKeyPath: "../../testdata/test.05.key.pem"}
	_, err = config.TLSListener()
	if err == nil {
		t.Errorf("expected error: %s", expectedErr)
	}

}
