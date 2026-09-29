/*
 * Copyright 2026 The Trickster Authors
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
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"

	"github.com/stretchr/testify/require"
)

// stickySecret is the key the tests' sticky tokens are signed with
const stickySecret = "integration-sticky-secret-0123456789"

// stickyClient is one client of a sticky ALB, which sends back the token it was last issued
type stickyClient struct {
	a      *strategyALB
	cookie string
	issued int
	n      int
}

// get sends one instant query, distinct per request so no cache answers it, and returns the
// response code and the index of the stub whose series answered it, or -1 when none did
func (c *stickyClient) get(t *testing.T) (int, int) {
	t.Helper()
	c.n++
	q := url.Values{"query": {fmt.Sprintf(`up{sticky="%p-%d"}`, c, c.n)}}
	u := fmt.Sprintf("http://127.0.0.1:%d/%s/api/v1/query?%s", c.a.front, c.a.name, q.Encode())
	req, err := http.NewRequest(http.MethodGet, u, nil)
	require.NoError(t, err)
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	if line := resp.Header.Get("Set-Cookie"); line != "" {
		c.cookie, _, _ = strings.Cut(line, ";")
		c.issued++
	}
	return resp.StatusCode, servedBy(body)
}

// servedBy returns the index of the stub whose series a response carries, or -1
func servedBy(body []byte) int {
	for i := range 10 {
		if strings.Contains(string(body), fmt.Sprintf(`"job":"p%d"`, i)) {
			return i
		}
	}
	return -1
}

func stickyALB(t *testing.T, sticky string) *strategyALB {
	t.Helper()
	extra := fmt.Sprintf("      sticky:\n        secret: %s\n%s", stickySecret, sticky)
	return startStrategyALB(t, "rr", extra, 3, nil)
}

// each client stays on its member, clients spread over the pool, and a session whose member fails
// moves once and stays where it moved, even after that member returns
func TestALBStickySessions(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	a := stickyALB(t, "")
	clients := make([]*stickyClient, 6)
	owners := make([]int, len(clients))
	for i := range clients {
		clients[i] = &stickyClient{a: a}
		code, owner := clients[i].get(t)
		require.Equal(t, http.StatusOK, code)
		owners[i] = owner
	}
	for round := range 5 {
		for i, c := range clients {
			code, served := c.get(t)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, owners[i], served, "client %d moved on round %d", i, round)
		}
	}
	for i, c := range clients {
		require.Equal(t, 1, c.issued, "client %d was issued %d tokens", i, c.issued)
	}
	spread := slices.Clone(owners)
	slices.Sort(spread)
	require.Greater(t, len(slices.Compact(spread)), 1, "every session landed on one member")

	c := clients[0]
	pinned := a.stubs[owners[0]]
	pinned.setUp(false)
	requireHealthState(t, a.health, fmt.Sprintf("prom%d", owners[0]), "unavailable", 10*time.Second)
	code, moved := c.get(t)
	require.Equal(t, http.StatusOK, code)
	require.NotEqual(t, owners[0], moved)
	require.Equal(t, 2, c.issued, "the moved session was not issued a token for its new member")
	pinned.setUp(true)
	requireHealthState(t, a.health, fmt.Sprintf("prom%d", owners[0]), "available", 10*time.Second)
	for range 5 {
		code, served := c.get(t)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, moved, served, "the session went back to its first member")
	}
}

// with on_unavailable: reject, a session waits out its member's outage rather than moving
func TestALBStickySessionsReject(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	a := stickyALB(t, "        on_unavailable: reject\n")
	c := &stickyClient{a: a}
	code, owner := c.get(t)
	require.Equal(t, http.StatusOK, code)
	a.stubs[owner].setUp(false)
	requireHealthState(t, a.health, fmt.Sprintf("prom%d", owner), "unavailable", 10*time.Second)
	code, served := c.get(t)
	require.Equal(t, http.StatusServiceUnavailable, code)
	require.Equal(t, -1, served)
	a.stubs[owner].setUp(true)
	requireHealthState(t, a.health, fmt.Sprintf("prom%d", owner), "available", 10*time.Second)
	code, served = c.get(t)
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, owner, served)
	require.Equal(t, 1, c.issued)
}

// in table mode the ALB remembers each client by its key, and issues nothing
func TestALBStickyTable(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	a := startStrategyALB(t, "rr", "      sticky:\n        mode: table\n        table:\n          key: header:X-Client\n", 3, nil)
	owners := make(map[string]int)
	for round := range 4 {
		for i := range 6 {
			client := fmt.Sprintf("client-%d", i)
			code, served := tableGet(t, a, round*100+i, client)
			require.Equal(t, http.StatusOK, code)
			if round == 0 {
				owners[client] = served
				continue
			}
			require.Equal(t, owners[client], served, "%s moved on round %d", client, round)
		}
	}
}

// tableGet sends one instant query as a client identified by its header, and returns the response
// code and the index of the stub that answered it
func tableGet(t *testing.T, a *strategyALB, n int, client string) (int, int) {
	t.Helper()
	q := url.Values{"query": {fmt.Sprintf(`up{table="%s-%d"}`, client, n)}}
	u := fmt.Sprintf("http://127.0.0.1:%d/%s/api/v1/query?%s", a.front, a.name, q.Encode())
	req, err := http.NewRequest(http.MethodGet, u, nil)
	require.NoError(t, err)
	req.Header.Set("X-Client", client)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Empty(t, resp.Header.Get("Set-Cookie"), "table mode issued a cookie")
	return resp.StatusCode, servedBy(body)
}

// namedUpgradeOrigin switches protocols, naming itself in the 101, and echoes a line back
func namedUpgradeOrigin(t *testing.T, name string) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\n" +
			"Upgrade: websocket\r\nX-Member: " + name + "\r\n\r\n")
		_ = brw.Flush()
		if line, err := brw.ReadString('\n'); err == nil {
			_, _ = brw.WriteString("echo:" + line)
			_ = brw.Flush()
		}
	}))
	t.Cleanup(s.Close)
	return s.URL
}

// addALB registers an ALB of the mechanism over the members; sticky, when set, is its sticky block
func addALB(name, mechanism string, sticky *so.Options, members ...string) func(*tkconfig.Config) {
	return func(c *tkconfig.Config) {
		o := bo.New()
		o.Name = name
		o.Provider = providers.ALB
		o.ALBOptions = &ao.Options{MechanismName: mechanism, Pool: ao.Members(members...), Sticky: sticky}
		c.Backends[name] = o
	}
}

// upgradeThrough opens a tunnel through Trickster, sending cookie when it is set, and returns the
// 101's headers once the tunnel has echoed a line
func upgradeThrough(t *testing.T, addr, path, cookie string) http.Header {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(20*time.Second)))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
	if cookie != "" {
		req += "Cookie: " + cookie + "\r\n"
	}
	_, err = conn.Write([]byte(req + "\r\n"))
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	_, err = conn.Write([]byte("ping\n"))
	require.NoError(t, err)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo:ping\n", line)
	return resp.Header
}

// a session opened by a protocol switch through Trickster's own proxy gets its token in the 101,
// and its reconnects reach the member it began on
func TestALBStickyUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	h := configHarness(t,
		addPassthroughBackend("ws-a", namedUpgradeOrigin(t, "ws-a")),
		addPassthroughBackend("ws-b", namedUpgradeOrigin(t, "ws-b")),
		addALB("wsalb", "rr", &so.Options{}, "ws-a", "ws-b"))
	h.start(t)
	first := upgradeThrough(t, h.BaseAddr, "/wsalb/socket", "")
	cookie, _, _ := strings.Cut(first.Get("Set-Cookie"), ";")
	require.True(t, strings.HasPrefix(cookie, so.DefaultCookieName+"="), "the 101 carried no token: %v", first)
	member := first.Get("X-Member")
	require.NotEmpty(t, member)
	for range 4 {
		again := upgradeThrough(t, h.BaseAddr, "/wsalb/socket", cookie)
		require.Equal(t, member, again.Get("X-Member"), "a reconnect left its member")
		require.Empty(t, again.Get("Set-Cookie"))
	}
}
