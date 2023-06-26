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

// Package proxy provides all proxy services for Trickster
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/common/sigv4"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
)

// NewHTTPClient returns an HTTP client configured to the specifications of the
// running Trickster config.
func NewHTTPClient(o *bo.Options) (*http.Client, error) {

	if o == nil {
		return nil, nil
	}

	var TLSConfig *tls.Config

	if o.TLS != nil {
		TLSConfig = &tls.Config{InsecureSkipVerify: o.TLS.InsecureSkipVerify}

		if o.TLS.ClientCertPath != "" && o.TLS.ClientKeyPath != "" {
			// load client cert
			cert, err := tls.LoadX509KeyPair(o.TLS.ClientCertPath, o.TLS.ClientKeyPath)
			if err != nil {
				return nil, err
			}
			TLSConfig.Certificates = []tls.Certificate{cert}
		}

		if o.TLS.CertificateAuthorityPaths != nil && len(o.TLS.CertificateAuthorityPaths) > 0 {

			// credit snippet to https://forfuncsake.github.io/post/2017/08/trust-extra-ca-cert-in-go-app/
			// Get the SystemCertPool, continue with an empty pool on error
			rootCAs, _ := x509.SystemCertPool()
			if rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}

			for _, path := range o.TLS.CertificateAuthorityPaths {
				// Read in the cert file
				certs, err := os.ReadFile(path)
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

	client := &http.Client{
		Timeout: o.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial:                (&net.Dialer{KeepAlive: time.Duration(o.KeepAliveTimeoutMS) * time.Millisecond}).Dial,
			MaxIdleConns:        o.MaxIdleConns,
			MaxIdleConnsPerHost: o.MaxIdleConns,
			TLSClientConfig:     TLSConfig,
		},
	} 

	if o.SigV4 != nil {
		var err error
		client.Transport, err = sigv4.NewSigV4RoundTripper(o.SigV4, client.Transport)
		if err != nil {
			return nil, err
		}
	}

	return client, nil
}
