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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stickyRequests is how many requests a session must hold across; the pools alternate, so a
// session that did not hold would leave well before this many
const stickyRequests = 20

// answeredBy names what served a whoami response: the canary names itself, and every pod
// reports its own hostname
func answeredBy(body string) string {
	for line := range strings.Lines(body) {
		if h, ok := strings.CutPrefix(strings.TrimSpace(line), "Hostname: "); ok {
			return h
		}
	}
	return ""
}

// serviceOf names the Service a whoami response came from on the Gateway fixture
func serviceOf(body string) string {
	if strings.Contains(body, "Name: canary") {
		return "webecho-canary"
	}
	return "webecho"
}

// holdsSession sends requests carrying the session header and requires each to be answered where
// the first was, with no new token once the session is established
func holdsSession(t *testing.T, addr, host, path string, session http.Header, want string,
	by func(string) string,
) {
	t.Helper()
	for range stickyRequests {
		resp, body := hostGet(t, addr, host, path, session)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		require.Equal(t, want, by(body))
		require.Empty(t, resp.Header.Values("Set-Cookie"), "a request its session served is issued nothing")
	}
}

// spreads requires requests without a session to reach more than one place, so a held session
// is the persistence at work rather than a pool with one member
func spreads(t *testing.T, addr, host, path string, by func(string) string) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		seen := make(map[string]bool)
		for range stickyRequests {
			resp, body := hostGet(t, addr, host, path)
			assert.Equal(collect, http.StatusOK, resp.StatusCode)
			seen[by(body)] = true
		}
		assert.Greater(collect, len(seen), 1, "requests without a session reached only %v", seen)
	}, 2*time.Minute, time.Second)
}

// replaceControllerPods restarts the Gateway controller and waits until none of its old pods is
// left, since rollout status can answer for the previous rollout before it sees the restart
func replaceControllerPods(t *testing.T) {
	t.Helper()
	const selector = "app=trickster-gateway"
	old := strings.Fields(kubectlKind(t, "", "get", "pods", "-l", selector, "-o", "name"))
	require.NotEmpty(t, old)
	kubectlKind(t, "", "rollout", "restart", "deployment/trickster-gateway")
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		out, err := kubectlTry("", "get", "pods", "-l", selector, "-o", "name")
		if !assert.NoError(collect, err) {
			return
		}
		pods := strings.Fields(out)
		assert.NotEmpty(collect, pods)
		for _, p := range old {
			assert.NotContains(collect, pods, p, "an old controller pod is still running")
		}
	}, 3*time.Minute, time.Second)
	kubectlKind(t, "", "rollout", "status", "deployment/trickster-gateway", "--timeout=180s")
}

func TestGatewaySessionPersistenceKind(t *testing.T) {
	skipUnlessKind(t)
	const (
		host       = "sticky.example.com"
		cookieName = "gw-session"
		headerName = "X-Gw-Session"
	)
	waitForTrickster(t, gatewayMetricsAddr)
	waitRoute(t, gatewayHTTPAddr, host, "/", http.StatusOK, 2*time.Minute)
	spreads(t, gatewayHTTPAddr, host, "/", serviceOf)

	t.Run("cookie", func(t *testing.T) {
		resp, body := hostGet(t, gatewayHTTPAddr, host, "/")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		var session *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == cookieName {
				session = c
			}
		}
		require.NotNil(t, session, "the route's sessionName names the cookie")
		require.Zero(t, session.MaxAge, "a Session lifetime cookie carries no Max-Age")
		held := http.Header{"Cookie": {cookieName + "=" + session.Value}}
		holdsSession(t, gatewayHTTPAddr, host, "/", held, serviceOf(body), serviceOf)

		// the class's Secret keys the tokens, so a new controller pod honors the old one's
		replaceControllerPods(t)
		waitRoute(t, gatewayHTTPAddr, host, "/", http.StatusOK, 2*time.Minute)
		holdsSession(t, gatewayHTTPAddr, host, "/", held, serviceOf(body), serviceOf)
	})

	t.Run("header", func(t *testing.T) {
		resp, body := hostGet(t, gatewayHTTPAddr, host, "/header")
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		token := resp.Header.Get(headerName)
		require.NotEmpty(t, token, "the token is issued in the header the sessionName names")
		holdsSession(t, gatewayHTTPAddr, host, "/header", http.Header{headerName: {token}},
			serviceOf(body), serviceOf)
	})
}

func TestIngressStickyKind(t *testing.T) {
	skipUnlessKind(t)
	const (
		frontAddr   = "127.0.0.1:30083"
		metricsAddr = "127.0.0.1:30084"
		host        = "sticky-endpoint.example.com"
	)
	waitForTrickster(t, metricsAddr)
	waitRoute(t, frontAddr, host, "/", http.StatusOK, 2*time.Minute)
	spreads(t, frontAddr, host, "/", answeredBy)

	// the endpoint ALB names its own cookie, and a client presenting it stays on its pod
	resp, body := hostGet(t, frontAddr, host, "/")
	require.Equal(t, http.StatusOK, resp.StatusCode, body)
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "trickster_sticky_") {
			session = c
		}
	}
	require.NotNil(t, session, "the sticky annotation issues a cookie")
	holdsSession(t, frontAddr, host, "/", http.Header{"Cookie": {session.Name + "=" + session.Value}},
		answeredBy(body), answeredBy)
}
