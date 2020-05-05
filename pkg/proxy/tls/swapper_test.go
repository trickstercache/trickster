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
	"crypto/tls"
	"testing"
)

func getSwapper(id string, t *testing.T) (*CertSwapper, *tls.Config) {

	var err error

	options := tlsConfig(id)

	tlscfg1 := &tls.Config{NextProtos: []string{"h2"}}
	tlscfg1.Certificates = make([]tls.Certificate, 1)
	tlscfg1.Certificates[0], err =
		tls.LoadX509KeyPair(options.FullChainCertPath, options.PrivateKeyPath)
	if err != nil {
		t.Fatal(err)
	}

	return NewSwapper(tlscfg1.Certificates), tlscfg1

}

func TestGetSetCert(t *testing.T) {

	chi := &tls.ClientHelloInfo{}
	sw, cfg := getSwapper("01", t)
	_, err := sw.GetCert(chi)
	if err != nil {
		t.Error(err)
	}

	_, cfg2 := getSwapper("02", t)
	sw.Certificates = append(sw.Certificates, cfg2.Certificates...)
	_, err = sw.GetCert(chi)
	if err != nil {
		t.Error(err)
	}

	sw.Certificates = nil
	_, err = sw.GetCert(chi)
	if err == nil || err.Error() != "tls: no certificates configured" {
		t.Errorf("expected error for no certificates configured. %s", err.Error())
	}
	sw.SetCerts(cfg.Certificates)
	_, err = sw.GetCert(chi)
	if err != nil {
		t.Error(err)
	}
}
