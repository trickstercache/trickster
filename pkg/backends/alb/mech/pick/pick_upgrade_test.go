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

package pick

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lt"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"

	"github.com/stretchr/testify/require"
)

const memberHeader = "X-Member"

// upgradingOrigin names itself on every answer. It switches protocols for a request that asks,
// handing out a session id in the 101 as sid sets it, and echoes a line over the tunnel.
func upgradingOrigin(t *testing.T, name string, sid func(http.Header, string)) *url.URL {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "" {
			w.Header().Set(memberHeader, name)
			return
		}
		h := http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, memberHeader: {name}}
		if sid != nil {
			sid(h, fmt.Sprintf("%s-%d", name, n.Add(1)))
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
		_ = h.Write(brw)
		_, _ = brw.WriteString("\r\n")
		_ = brw.Flush()
		if line, err := brw.ReadString('\n'); err == nil {
			_, _ = brw.WriteString("echo:" + line)
			_ = brw.Flush()
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	return u
}

// proxied is a member that relays to an origin as the passthrough engine does, upgrades included
func proxied(t *testing.T, name string, sid func(http.Header, string)) member {
	return named(t, name, httputil.NewSingleHostReverseProxy(upgradingOrigin(t, name, sid)))
}

// tunnel is one switched connection through the ALB, opened with the request's extra headers
type tunnel struct {
	conn net.Conn
	br   *bufio.Reader
	resp *http.Response
}

func upgrade(t *testing.T, addr string, header http.Header) *tunnel {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	req := "GET /socket HTTP/1.1\r\nHost: " + addr + "\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"
	for k, vs := range header {
		for _, v := range vs {
			req += k + ": " + v + "\r\n"
		}
	}
	_, err = conn.Write([]byte(req + "\r\n"))
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	return &tunnel{conn: conn, br: br, resp: resp}
}

// echo proves the tunnel carries bytes both ways, then closes it
func (tn *tunnel) echo(t *testing.T) {
	t.Helper()
	_, err := tn.conn.Write([]byte("ping\n"))
	require.NoError(t, err)
	line, err := tn.br.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "echo:ping\n", line)
	tn.conn.Close()
}

// the token of a session opened by a protocol switch goes out with the 101, which the upgrade
// handler writes itself on the hijacked connection
func TestStickyTokenTravelsWithAnUpgrade(t *testing.T) {
	h := stickyALB(t, "{}", rr.New(), proxied(t, "a", nil), proxied(t, "b", nil), proxied(t, "c", nil))
	srv := httptest.NewServer(h)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	first := upgrade(t, addr, nil)
	cookie, _, _ := strings.Cut(first.resp.Header.Get("Set-Cookie"), ";")
	require.True(t, strings.HasPrefix(cookie, cookieName+"="), "the 101 carried no token: %v", first.resp.Header)
	member := first.resp.Header.Get(memberHeader)
	first.echo(t)
	// every reconnect lands where the session began, and is issued nothing new
	for range 4 {
		again := upgrade(t, addr, http.Header{"Cookie": {cookie}})
		require.Equal(t, member, again.resp.Header.Get(memberHeader))
		require.Empty(t, again.resp.Header.Get("Set-Cookie"))
		again.echo(t)
	}
}

// a session id an upstream hands out in its 101 is learned as the 101 is written, so the client's
// other requests reach that member while the tunnel is still open
func TestStickyTableLearnsFromAnUpgrade(t *testing.T) {
	for form, tc := range map[string]struct {
		doc  string
		set  func(http.Header, string)
		read func(http.Header) string
		send func(*http.Request, string)
	}{
		"header": {
			doc:  "mode: table\ntable: {key: header:X-Session, learn: response}\n",
			set:  func(h http.Header, id string) { h.Set("X-Session", id) },
			read: func(h http.Header) string { return h.Get("X-Session") },
			send: func(r *http.Request, id string) { r.Header.Set("X-Session", id) },
		},
		"cookie": {
			doc: "mode: table\ntable: {key: cookie:SID, learn: response}\n",
			set: func(h http.Header, id string) { h.Add("Set-Cookie", "SID="+id+"; Path=/") },
			read: func(h http.Header) string {
				c, _, _ := strings.Cut(strings.TrimPrefix(h.Get("Set-Cookie"), "SID="), ";")
				return c
			},
			send: func(r *http.Request, id string) { r.AddCookie(&http.Cookie{Name: "SID", Value: id}) },
		},
	} {
		t.Run(form, func(t *testing.T) {
			h := stickyALB(t, tc.doc, rr.New(),
				proxied(t, "a", tc.set), proxied(t, "b", tc.set), proxied(t, "c", tc.set))
			srv := httptest.NewServer(h)
			defer srv.Close()
			addr := strings.TrimPrefix(srv.URL, "http://")
			for range 3 {
				tn := upgrade(t, addr, nil)
				id, member := tc.read(tn.resp.Header), tn.resp.Header.Get(memberHeader)
				require.NotEmpty(t, id)
				for range 3 {
					r, err := http.NewRequest(http.MethodGet, srv.URL+"/api", nil)
					require.NoError(t, err)
					tc.send(r, id)
					resp, err := http.DefaultClient.Do(r)
					require.NoError(t, err)
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					require.Equal(t, member, resp.Header.Get(memberHeader), "session %s left its member", id)
				}
				tn.echo(t)
			}
		})
	}
}

// a timed ALB times an upgrade to its 101, and its accounting ends with the tunnel
func TestTimedDispatchOfAnUpgrade(t *testing.T) {
	m := proxied(t, "a", nil)
	h := New(names.MechanismLT, lt.New(lt.Options{}), Options{GoodCodes: options.DefaultLTStatusCodes().Compile()}).(*handler)
	h.SetPool(poolOf(t, m))
	srv := httptest.NewServer(h)
	defer srv.Close()
	tn := upgrade(t, strings.TrimPrefix(srv.URL, "http://"), nil)
	tn.echo(t)
	st := m.target.Member().Stats()
	require.Eventually(t, func() bool { return st.Inflight() == 0 }, 5*time.Second, 5*time.Millisecond)
	require.NotZero(t, st.Latency(), "the switch was not timed")
	require.Zero(t, st.Failures())
}

// hijackable is a writer that hands over one end of a pipe
type hijackable struct {
	plainWriter
	conn net.Conn
}

func (h *hijackable) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

func TestHijackThroughTheWriter(t *testing.T) {
	// a writer that cannot be hijacked says so, and nothing counts as written
	var plain plainWriter
	fw := getWriter(&plain, lb.Pick{})
	defer putWriter(fw)
	if _, _, err := fw.Hijack(); err == nil || fw.wrote {
		t.Errorf("hijack of a plain writer: %v, wrote %v", err, fw.wrote)
	}
	// once the answer has begun, a hijack only hands the connection over
	near, far := net.Pipe()
	defer near.Close()
	defer far.Close()
	written := getWriter(&hijackable{conn: near}, lb.Pick{})
	defer putWriter(written)
	written.WriteHeader(http.StatusOK)
	conn, _, err := written.Hijack()
	if err != nil || conn != near || written.code != http.StatusOK {
		t.Errorf("hijack after a write: %v, code %d", err, written.code)
	}
}
