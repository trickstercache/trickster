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

// Package testing provides functionality for use when conducting tests
package testing

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	tc "github.com/Comcast/trickster/internal/proxy/context"
	th "github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/request"
	tl "github.com/Comcast/trickster/internal/util/log"
	tr "github.com/Comcast/trickster/internal/util/tracing/registration"
	"github.com/Comcast/trickster/pkg/promsim"
	"github.com/Comcast/trickster/pkg/rangesim"
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
	DefaultPathConfigs func(*config.OriginConfig) map[string]*config.PathConfig,
	respCode int, respBody string, respHeaders map[string]string,
	originType, urlPath, logLevel string,
) (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *http.Client, error) {

	isBasicTestServer := false

	var ts *httptest.Server
	if originType == "promsim" {
		ts = promsim.NewTestServer()
		originType = "prometheus"
	} else if originType == "rangesim" {
		ts = rangesim.NewTestServer()
		originType = "rpc"
	} else {
		isBasicTestServer = true
		ts = NewTestServer(respCode, respBody, respHeaders)
	}

	args := []string{"-origin-url", ts.URL, "-origin-type", originType, "-log-level", logLevel}
	if configFile != "" {
		args = append(args, []string{"-config", configFile}...)
	}

	conf, _, err := config.Load("trickster", "test", args)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, tl.ConsoleLogger("error"))
	cache, ok := caches["default"]
	if !ok {
		return nil, nil, nil, nil, err
	}

	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", ts.URL+urlPath, nil)

	oc := conf.Origins["default"]
	p := NewTestPathConfig(oc, DefaultPathConfigs, urlPath)

	tracer, _, _ := tr.Init(oc.TracingConfig, tl.ConsoleLogger("error"))
	// TODO worry about running closures for cleanup once the test is complete
	oc.TracingConfig.Tracer = tracer

	if !isBasicTestServer && respHeaders != nil {
		p.ResponseHeaders = respHeaders
	}

	rsc := request.NewResources(oc, p, cache.Configuration(), cache, nil, tl.ConsoleLogger("error"))
	r = r.WithContext(tc.WithResources(r.Context(), rsc))

	c := NewTestWebClient()

	return ts, w, r, c, nil
}

// NewTestPathConfig returns a path config based on the provided parameters
func NewTestPathConfig(
	oc *config.OriginConfig,
	DefaultPathConfigs func(*config.OriginConfig) map[string]*config.PathConfig,
	urlPath string,
) *config.PathConfig {
	var paths map[string]*config.PathConfig
	if DefaultPathConfigs != nil {
		paths = DefaultPathConfigs(oc)
	}

	oc.Paths = paths

	p := &config.PathConfig{}
	if len(paths) > 0 {
		if p2, ok := paths[urlPath]; ok {
			p = p2
		} else {
			p = paths["/"]
		}
	}

	return p
}
