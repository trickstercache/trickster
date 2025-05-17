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

// Package testing provides functionality for use when conducting tests
package testing

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	tr "github.com/trickstercache/trickster/v2/pkg/observability/tracing/registry"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	th "github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"

	"github.com/trickstercache/mockster/pkg/testutil"
)

const (
	// Epoch2020 is the epoch value representing 1 January 2020 00:00:00 UTC
	Epoch2020 int64 = 1577836800

	// Provider Names
	PrometheusBackendProvider = "prometheus"
	PromSimBackendProvider    = "promsim"
	RangeSimBackendProvider   = "rangesim"
	RPCBackendProvider        = providers.ReverseProxyCacheShort
)

// Time2020 is the Time.Time representing 1 January 2020 00:00:00 UTC
var Time2020 = time.Unix(Epoch2020, 0)

// ErrTest is a Test Error
var ErrTest = errors.New("test error")

// this actively sets the ApplicationName for testing purposes
// do not import this package from main or any of its recursive imports
func init() {
	appinfo.Name = "trickster-unit-tests"
}

// NewTestServer returns a new httptest.Server that responds with the provided code, body and headers
func NewTestServer(responseCode int, responseBody string, headers map[string]string) *httptest.Server {
	handler := func(w http.ResponseWriter, _ *http.Request) {
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
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Dial:                (&net.Dialer{KeepAlive: 5 * time.Minute}).Dial,
			MaxIdleConns:        20,
			MaxIdleConnsPerHost: 20,
		},
	}
}

// NewTestInstance will start a trickster
func NewTestInstance(
	configFile string,
	defaultPathConfigs func(*bo.Options) map[string]*po.Options,
	respCode int, respBody string, respHeaders map[string]string,
	backendProvider, urlPath, logLevel string,
) (*httptest.Server, *httptest.ResponseRecorder, *http.Request, *http.Client, error) {

	isBasicTestServer := false

	var ts *httptest.Server
	switch backendProvider {
	case PromSimBackendProvider:
		ts = testutil.NewTestServer()
		backendProvider = PrometheusBackendProvider
	case RangeSimBackendProvider:
		ts = testutil.NewTestServer()
		backendProvider = RPCBackendProvider
	default:
		isBasicTestServer = true
		ts = NewTestServer(respCode, respBody, respHeaders)
	}

	args := []string{"-origin-url", ts.URL, "-provider", backendProvider, "-log-level", logLevel}
	if configFile != "" {
		args = append(args, []string{"-config", configFile}...)
	}

	conf, err := config.Load(args)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("could not load configuration: %s", err.Error())
	}

	logger.SetLogger(logging.ConsoleLogger(level.Error))
	caches := cr.LoadCachesFromConfig(conf)
	cache, ok := caches["default"]
	if !ok {
		return nil, nil, nil, nil, err
	}

	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, ts.URL+urlPath, nil)

	o := conf.Backends["default"]
	p := NewTestPathConfig(o, defaultPathConfigs, urlPath)

	var tracer *tracing.Tracer

	logger.SetLogger(logging.ConsoleLogger(level.Error))
	if o.TracingConfigName != "" {
		if tc, ok := conf.TracingConfigs[o.TracingConfigName]; ok {
			tracer, _ = tr.GetTracer(tc, true)
		}
	} else {
		tracer = NewTestTracer()
	}

	if !isBasicTestServer && respHeaders != nil {
		p.ResponseHeaders = respHeaders
	}

	rsc := request.NewResources(o, p, cache.Configuration(), cache, nil, tracer)
	r = r.WithContext(tc.WithResources(r.Context(), rsc))

	c := NewTestWebClient()

	return ts, w, r, c, nil
}

// NewTestPathConfig returns a path config based on the provided parameters
func NewTestPathConfig(
	o *bo.Options,
	defaultPathConfigs func(*bo.Options) map[string]*po.Options,
	urlPath string,
) *po.Options {
	var paths map[string]*po.Options
	if defaultPathConfigs != nil {
		paths = defaultPathConfigs(o)
	}

	o.Paths = paths

	p := &po.Options{}
	if len(paths) > 0 {
		if p2, ok := paths[urlPath]; ok {
			p = p2
		} else {
			p = paths["/"]
		}
	}

	return p
}

// NewTestTracer returns a standard out tracer for testing purposes
func NewTestTracer() *tracing.Tracer {
	logger.SetLogger(logging.ConsoleLogger(level.Warn))
	tc := to.New()
	tc.Name = "test"
	tc.Provider = "stdout"
	tracer, _ := tr.GetTracer(tc, true)
	return tracer
}

// BasicHTTPHandler is a basic HTTP Handler for use in unit e
func BasicHTTPHandler(w http.ResponseWriter, _ *http.Request) {
	if w == nil {
		return
	}
	h := w.Header()
	h.Set("Test-Header", "Trickster")
	h.Set(th.NameLastModified, "Wed, 01 Jan 2020 00:00:00 UTC")
	h.Set(th.NameTricksterResult, "engine=none")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{}"))
}

// NewTestRewriterInstructions returns a set of example Rewriter Instructions
// for use in unit testing
func NewTestRewriteInstructions() rewriter.RewriteInstructions {

	trwl := options.RewriteList{
		[]string{"method", "set", "POST"},
		[]string{"host", "set", "example.com:9090"},
		[]string{"host", "replace", "example.com", "trickstercache.org"},
		[]string{"port", "delete"},
		[]string{"port", "set", "8000"},
		[]string{"port", "replace", "000", "480"},
		[]string{"scheme", "set", "https"},
		[]string{"hostname", "set", "example.com"},
		[]string{"hostname", "replace", "example.com", "trickstercache.org"},
	}

	ri, _ := rewriter.ParseRewriteList(trwl)

	return ri
}
