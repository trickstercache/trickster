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

package proxy

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/gorilla/mux"
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

	const caFile = "../../testdata/test.rootca.pem"
	const caFileInvalid1 = caFile + ".invalid"
	const caFileInvalid2 = "../../testdata/test.06.cert.pem"

	// test good originconfig, no CA
	oc := config.NewOriginConfig()
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
	oc.TLS.ClientCertPath = "../../testdata/test.01.cert.pem"
	oc.TLS.ClientKeyPath = "../../testdata/test.01.key.pem"
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

func TestNewListenerErr(t *testing.T) {
	config.NewConfig()
	l, err := NewListener("-", 0, 0, nil)
	if err == nil {
		l.Close()
		t.Errorf("expected error: %s", `listen tcp: lookup -: no such host`)
	}
}

func TestNewListenerTLS(t *testing.T) {

	c := config.NewConfig()
	oc := c.Origins["default"]
	c.Frontend.ServeTLS = true

	tc := oc.TLS
	oc.TLS.ServeTLS = true
	tc.FullChainCertPath = "../../testdata/test.01.cert.pem"
	tc.PrivateKeyPath = "../../testdata/test.01.key.pem"

	tlsConfig, err := c.TLSCertConfig()
	if err != nil {
		t.Error(err)
	}

	l, err := NewListener("", 0, 0, tlsConfig)
	defer l.Close()
	if err != nil {
		t.Error(err)
	}

}

func TestListenerConnectionLimitWorks(t *testing.T) {

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "hello!")
	}
	es := httptest.NewServer(http.HandlerFunc(handler))
	defer es.Close()

	_, _, err := config.Load("trickster", "test", []string{"-origin-url", es.URL, "-origin-type", "prometheus"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	tt := []struct {
		Name             string
		ListenPort       int
		ConnectionsLimit int
		Clients          int
		expectedErr      string
	}{
		{
			"Without connection limit",
			34001,
			0,
			1,
			"",
		},
		{
			"With connection limit of 10",
			34002,
			10,
			10,
			"",
		},
		{
			"With connection limit of 1, but with 10 clients",
			34003,
			1,
			10,
			"Get http://localhost:34003/: net/http: request canceled (Client.Timeout exceeded while awaiting headers)",
		},
	}

	http.DefaultClient.Timeout = 100 * time.Millisecond

	for _, tc := range tt {
		t.Run(tc.Name, func(t *testing.T) {
			l, err := NewListener("", tc.ListenPort, tc.ConnectionsLimit, nil)
			defer l.Close()

			go func() {
				http.Serve(l, mux.NewRouter())
			}()

			if err != nil {
				t.Fatalf("failed to create listener: %s", err)
			}

			for i := 0; i < tc.Clients; i++ {
				r, err := http.NewRequest("GET", fmt.Sprintf("http://localhost:%d/", tc.ListenPort), nil)
				if err != nil {
					t.Fatalf("failed to create request: %s", err)
				}
				res, err := http.DefaultClient.Do(r)
				if err != nil {
					if fmt.Sprintf("%s", err) != tc.expectedErr {
						t.Fatalf("unexpected error when executing request: %s", err)
					}
					continue
				}
				defer func() {
					io.Copy(ioutil.Discard, res.Body)
					res.Body.Close()
				}()
			}

		})
	}
}
