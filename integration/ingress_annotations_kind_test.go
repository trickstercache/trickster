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
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIngressKind(t *testing.T) {
	skipUnlessKind(t)
	const addr = "127.0.0.1:30082"
	waitRoute(t, addr, "shop.example.com", "/api/hello", http.StatusOK, 2*time.Minute)
	resp, body := hostGet(t, addr, "shop.example.com", "/api/hello")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, "GET /hello HTTP/1.1", "rewrite-target replaces the matched prefix")
	require.Contains(t, body, "Hostname: webecho-")
	require.Equal(t, "shop", resp.Header.Get("X-Trickster-Ingress"), "the response-headers annotation applies")
	resp, _ = hostGet(t, addr, "shop.example.com", "/other")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "a path the Ingress does not claim is not served")
	resp, _ = hostGet(t, addr, "unclaimed.example.com", "/api/hello")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "a host the Ingress does not claim is not served")
}

func TestIngressAnnotationsKind(t *testing.T) {
	skipUnlessKind(t)
	const host = "annotated.example.com"
	waitRoute(t, gatewayIngressAddr, host, "/legacy/hello", http.StatusOK, 2*time.Minute)

	resp, body := hostGet(t, gatewayIngressAddr, host, "/legacy/hello?x=1")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, "GET /hello?x=1 HTTP/1.1", "the regex rewrite keeps the second capture")
	require.Equal(t, "trickster", resp.Header.Get("X-Served-By"),
		"the response-headers annotation applies, not the foreign controller's")
	resp, body = hostGet(t, gatewayIngressAddr, host, "/legacy")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	require.Contains(t, body, "GET / HTTP/1.1", "the bare prefix rewrites to the root")
	resp, _ = hostGet(t, gatewayIngressAddr, host, "/other")
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "a path outside the pattern is not served")

	// the unrecognized trickstercache.org annotation was rejected with an Event
	// naming it, and the foreign controller's annotations drew no rejection
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		out := kubectlKind(t, "", "get", "events", "--field-selector",
			"reason=InvalidAnnotation,involvedObject.kind=Ingress,involvedObject.name=annotated",
			"-o", "jsonpath={.items[*].message}")
		assert.Contains(collect, out, "trickstercache.org/proxy-read-timeout")
		assert.NotContains(collect, out, "acme.example.com")
	}, 2*time.Minute, time.Second, "no InvalidAnnotation Event for the unrecognized annotation")
}
