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

package integration

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCachePolicyKind is the phase 6 Kubernetes controller scenario: a real
// Prometheus behind Ingresses served by the in-cluster trickster-ingress
// controller (deployed by `make kind-integration-ingress`; see
// kind/README.md), routed through the prometheus provider by a
// TricksterCachePolicy on its Service. A range query is served by the Delta
// Proxy Cache rather than proxied, the same query is a cache hit the second
// time, the Ingress whose policy hides the result header carries none, and a
// policy naming a cache no file backend references is accepted, so its
// Ingress is a Delta Proxy Cache key miss and then a hit rather than a proxy.
//
// Gated on TRICKSTER_KIND_TEST=1 like TestALBDiscoveryKind, and run after it
// by the integration-kind CI job.
func TestCachePolicyKind(t *testing.T) {
	if os.Getenv("TRICKSTER_KIND_TEST") != "1" {
		t.Skip("kind scenario runs only with TRICKSTER_KIND_TEST=1")
	}

	const (
		frontAddr  = "127.0.0.1:30082"
		host       = "prom.example.com"
		hiddenHost = "hidden.example.com"
		namedHost  = "named.example.com"
		result     = "X-Trickster-Result"
	)
	// a fixed range, so the second request is the first one's cache object
	end := time.Now().Truncate(time.Minute)
	query := url.Values{
		"query": {"up"},
		"start": {strconv.FormatInt(end.Add(-5*time.Minute).Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {"15"},
	}
	rangeURL := "http://" + frontAddr + "/api/v1/query_range?" + query.Encode()

	get := func(host, target string) (*http.Response, string, error) {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return nil, "", err
		}
		req.Host = host
		resp, err := discoveryHTTPClient.Do(req)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, string(body), nil
	}

	// Prometheus takes a moment to become ready, and the route a moment to be programmed
	var first *http.Response
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		resp, body, err := get(host, rangeURL)
		if !assert.NoError(collect, err) {
			return
		}
		assert.Equal(collect, http.StatusOK, resp.StatusCode, body)
		first = resp
	}, 3*time.Minute, time.Second)

	res := first.Header.Get(result)
	require.Contains(t, res, "engine=DeltaProxyCache",
		"the provider's range query handler should answer, not the object proxy: %s", res)

	second, body, err := get(host, rangeURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, second.StatusCode, body)
	res = second.Header.Get(result)
	require.Contains(t, res, "status=hit", "the same range should be a cache hit: %s", res)

	// a predefined path outside the range handlers is still served through the provider
	info, body, err := get(host, "http://"+frontAddr+"/api/v1/status/buildinfo")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, info.StatusCode, body)
	require.NotEmpty(t, info.Header.Get(result))

	// the Ingress whose policy hides the header serves the same query without it
	hidden, body, err := get(hiddenHost, rangeURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, hidden.StatusCode, body)
	require.Empty(t, hidden.Header.Get(result),
		fmt.Sprintf("the policy hides the result header (got %q)", hidden.Header.Get(result)))

	// the policy naming a cache only generated backends use is accepted: the
	// route is a Delta Proxy Cache whose first query misses and second hits,
	// not a reverse proxy that reports proxy-only on every request
	named, body, err := get(namedHost, rangeURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, named.StatusCode, body)
	res = named.Header.Get(result)
	require.Contains(t, res, "engine=DeltaProxyCache",
		"the named cache's policy should be accepted and its route a DPC: %s", res)
	require.Contains(t, res, "status=kmiss", "the first query is a key miss: %s", res)
	named, body, err = get(namedHost, rangeURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, named.StatusCode, body)
	res = named.Header.Get(result)
	require.Regexp(t, `status=p?hit`, res, "the same range should be a cache hit: %s", res)

	// a host no Ingress claims is not served
	none, _, err := get("unclaimed.example.com", rangeURL)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, none.StatusCode)
}
