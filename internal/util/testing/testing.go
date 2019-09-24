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

package testing

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	th "github.com/Comcast/trickster/internal/proxy/headers"
	ct "github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/metrics"
	"github.com/Comcast/trickster/pkg/promsim"
)

// NewTestServer returns a new httptest.Server that responds with the provided code, body and headers
func NewTestServer(responseCode int, responseBody string, headers map[string]string) *httptest.Server {
	handler := func(w http.ResponseWriter, r *http.Request) {
		th.UpdateHeaders(w.Header(), headers)
		w.WriteHeader(responseCode)
		fmt.Fprint(w, responseBody)
	}
	s := httptest.NewServer(http.HandlerFunc(handler))
	return s
}

// NewTestWebClient returns a new *http.Client configured with reasonable defaults
func NewTestWebClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial:                (&net.Dialer{KeepAlive: 300 * time.Second}).Dial,
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 20,
		},
	}
}

// NewTestInstance will start a trickster
func NewTestInstance(
	configFile string,
	f1 func(*config.OriginConfig) (map[string]*config.PathConfig, []string),
	respCode int, respBody string, respHeaders map[string]string,
	originType, urlPath, logLevel string,
) (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *http.Client, error) {

	metrics.Init()

	var ts *httptest.Server
	if originType == "promsim" {
		ts = promsim.NewTestServer()
		originType = "prometheus"
	} else {
		ts = NewTestServer(respCode, respBody, respHeaders)
	}

	err := config.Load("trickster", "test", []string{"-origin-url", ts.URL, "-origin-type", originType, "-log-level", logLevel})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		return nil, nil, nil, nil, err
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", ts.URL+urlPath, nil)

	oc := config.Origins["default"]
	r = r.WithContext(ct.WithConfigs(r.Context(), oc, cache, nil))

	var paths map[string]*config.PathConfig
	if f1 != nil {
		paths, _ = f1(oc)
	}

	var p *config.PathConfig
	if len(paths) > 0 {
		if p2, ok := paths[urlPath]; ok {
			p = p2
		}
	}

	r = r.WithContext(ct.WithConfigs(r.Context(), oc, cache, p))

	c := NewTestWebClient()

	return ts, w, r, c, nil
}
