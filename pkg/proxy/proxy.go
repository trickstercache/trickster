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

// Package proxy provides all proxy services for Trickster
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"time"

	oo "github.com/tricksterproxy/trickster/pkg/backends/options"
)

// NewHTTPClient returns an HTTP client configured to the specifications of the
// running Trickster config.
func NewHTTPClient(oc *oo.Options) (*http.Client, error) {

	if oc == nil {
		return nil, nil
	}

	var TLSConfig *tls.Config

	if oc.TLS != nil {
		TLSConfig = &tls.Config{InsecureSkipVerify: oc.TLS.InsecureSkipVerify}

		if oc.TLS.ClientCertPath != "" && oc.TLS.ClientKeyPath != "" {
			// load client cert
			cert, err := tls.LoadX509KeyPair(oc.TLS.ClientCertPath, oc.TLS.ClientKeyPath)
			if err != nil {
				return nil, err
			}
			TLSConfig.Certificates = []tls.Certificate{cert}
		}

		if oc.TLS.CertificateAuthorityPaths != nil && len(oc.TLS.CertificateAuthorityPaths) > 0 {

			// credit snippet to https://forfuncsake.github.io/post/2017/08/trust-extra-ca-cert-in-go-app/
			// Get the SystemCertPool, continue with an empty pool on error
			rootCAs, _ := x509.SystemCertPool()
			if rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}

			for _, path := range oc.TLS.CertificateAuthorityPaths {
				// Read in the cert file
				certs, err := ioutil.ReadFile(path)
				if err != nil {
					return nil, err
				}
				// Append our cert to the system pool
				if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
					return nil, fmt.Errorf("unable to append to CA Certs from file %s", path)
				}
			}

			// Trust the augmented cert pool in our client
			TLSConfig.RootCAs = rootCAs
		}
	}

	return &http.Client{
		Timeout: oc.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial:                (&net.Dialer{KeepAlive: time.Duration(oc.KeepAliveTimeoutMS) * time.Millisecond}).Dial,
			MaxIdleConns:        oc.MaxIdleConns,
			MaxIdleConnsPerHost: oc.MaxIdleConns,
			TLSClientConfig:     TLSConfig,
		},
	}, nil

}
