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

package sticky

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/secret"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

var (
	clock = time.Unix(born, 0)
	other = Path{Hashes: [lb.MaxPickDepth]uint64{0x1111}, Depth: 1}
)

// persistence returns the http persistence that a sticky block configures on an ALB of its own
func persistence(t *testing.T, doc string) *HTTP {
	t.Helper()
	o := &options.Options{}
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	o.Secret = secret.Secret(key)
	require.NoError(t, o.Initialize())
	require.NoError(t, o.Validate())
	name := t.Name()
	t.Cleanup(func() { ForgetTablesExcept(func(n string, _ *Table) bool { return n != name }) })
	p, err := NewHTTP(name, o)
	require.NoError(t, err)
	return p
}

func count(t *testing.T, result string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.ALBStickyResults.WithLabelValues(t.Name(), result))
}

func request(mutate func(*http.Request)) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	if mutate != nil {
		mutate(r)
	}
	return r
}

// serve runs one request through the persistence: it reads the pins, sends the request down
// chosen, and finishes the response, whose headers it returns
func serve(p *HTTP, r *http.Request, now time.Time, chosen Path) (http.Header, *Session) {
	var s Session
	p.Begin(r, &s, now)
	s.Chosen = chosen
	h := http.Header{}
	p.Finish(h, r, &s)
	return h, &s
}

// tokenIn returns the token a response set in its cookie
func tokenIn(t *testing.T, h http.Header, name string) string {
	t.Helper()
	res := http.Response{Header: h}
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	t.Fatalf("no %s cookie in %v", name, h)
	return ""
}

func withCookie(name, value string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Add("Cookie", "theme=dark; "+name+"="+value) }
}

func TestNewHTTPNeedsInitializedOptions(t *testing.T) {
	if _, err := NewHTTP(albName, nil); err != ErrNotInitialized {
		t.Errorf("nil options: %v", err)
	}
	if _, err := NewHTTP(albName, &options.Options{}); err != ErrNotInitialized {
		t.Errorf("uninitialized options: %v", err)
	}
}

func TestCookieModeIssuesHonorsAndMoves(t *testing.T) {
	p := persistence(t, "{}")
	// a first request is a miss, and is issued a token for the member it was sent to
	h, _ := serve(p, request(nil), clock, leaf)
	line := h.Get("Set-Cookie")
	require.Equal(t, "; Max-Age=3600; Path=/; HttpOnly; SameSite=Lax",
		strings.TrimPrefix(line, options.DefaultCookieName+"="+tokenIn(t, h, options.DefaultCookieName)))
	tok := tokenIn(t, h, options.DefaultCookieName)
	require.Equal(t, 1.0, count(t, ResultMiss))

	// sent back, it pins the request, which reaching its member is a quiet hit
	later := clock.Add(10 * time.Minute)
	h, s := serve(p, request(withCookie(options.DefaultCookieName, tok)), later, leaf)
	require.Equal(t, leaf, s.Pins)
	require.Empty(t, h, "a request served by its pinned member was issued a token")
	require.Equal(t, 1.0, count(t, ResultHit))

	// moved to another member, the session starts over there with a new token
	h, _ = serve(p, request(withCookie(options.DefaultCookieName, tok)), later, other)
	moved, st := p.codec.Read(tokenIn(t, h, options.DefaultCookieName), later.Unix())
	require.Equal(t, Valid, st)
	require.Equal(t, Token{Path: other, Born: later.Unix(), Issued: later.Unix()}, moved)
	require.Equal(t, 1.0, count(t, ResultRepick))

	// a token past its ttl, and one that is not this ALB's, start a new session too
	h, s = serve(p, request(withCookie(options.DefaultCookieName, tok)), clock.Add(2*time.Hour), leaf)
	require.Zero(t, s.Pins.Depth)
	require.NotEmpty(t, h.Get("Set-Cookie"))
	require.Equal(t, 1.0, count(t, ResultExpired))
	foreign := codec(t, "another-alb", 0, 0).Mint(Token{Path: leaf, Born: born, Issued: born})
	h, s = serve(p, request(withCookie(options.DefaultCookieName, foreign)), later, leaf)
	require.Zero(t, s.Pins.Depth)
	require.NotEmpty(t, h.Get("Set-Cookie"))
	require.Equal(t, 1.0, count(t, ResultInvalid))
	_, _ = serve(p, request(withCookie(options.DefaultCookieName, "")), later, leaf)
	require.Equal(t, 2.0, count(t, ResultInvalid), "an empty cookie is an invalid token")
}

func TestCookieAttributes(t *testing.T) {
	overTLS := func(r *http.Request) { r.TLS = &tls.ConnectionState{} }
	for doc, want := range map[string]string{
		"{}": "; Max-Age=3600; Path=/; HttpOnly; SameSite=Lax",
		"ttl: 0\nidle: 10m\ncookie: {name: s, path: /app, domain: example.com, http_only: false, same_site: strict}": "; Path=/app; Domain=example.com; SameSite=Strict",
		"ttl: 1h\nidle: 10m\ncookie: {secure: \"true\", same_site: none}":                                            "; Max-Age=600; Path=/; HttpOnly; SameSite=None; Secure",
		"cookie: {lifetime: session}": "; Path=/; HttpOnly; SameSite=Lax",
	} {
		t.Run(doc, func(t *testing.T) {
			p := persistence(t, doc)
			h, _ := serve(p, request(nil), clock, leaf)
			name, _, _ := strings.Cut(h.Get("Set-Cookie"), "=")
			require.Equal(t, want, strings.TrimPrefix(h.Get("Set-Cookie"), name+"="+tokenIn(t, h, name)))
		})
	}
	// auto marks the cookie Secure when the request arrived over TLS, and false never does
	auto := persistence(t, "{}")
	h, _ := serve(auto, request(overTLS), clock, leaf)
	require.True(t, strings.HasSuffix(h.Get("Set-Cookie"), "; Secure"))
	never := persistence(t, "cookie: {secure: \"false\"}")
	h, _ = serve(never, request(overTLS), clock, leaf)
	require.NotContains(t, h.Get("Set-Cookie"), "Secure")
	// a browser drops a SameSite=None cookie without Secure, so it is Secure on a plaintext request too
	none := persistence(t, "cookie: {same_site: none, secure: \"true\"}")
	h, _ = serve(none, request(nil), clock, leaf)
	require.True(t, strings.HasSuffix(h.Get("Set-Cookie"), "; SameSite=None; Secure"), h.Get("Set-Cookie"))
}

func TestIdleRefresh(t *testing.T) {
	p := persistence(t, "ttl: 1h\nidle: 10m\n")
	h, _ := serve(p, request(nil), clock, leaf)
	tok := tokenIn(t, h, options.DefaultCookieName)
	// within half the idle timeout a hit is quiet; past it the token is issued again, still born
	// when the session began, so the ttl keeps running from then
	h, _ = serve(p, request(withCookie(options.DefaultCookieName, tok)), clock.Add(4*time.Minute), leaf)
	require.Empty(t, h)
	at := clock.Add(6 * time.Minute)
	h, _ = serve(p, request(withCookie(options.DefaultCookieName, tok)), at, leaf)
	refreshed, st := p.codec.Read(tokenIn(t, h, options.DefaultCookieName), at.Unix())
	require.Equal(t, Valid, st)
	require.Equal(t, Token{Path: leaf, Born: born, Issued: at.Unix()}, refreshed)
	require.Contains(t, h.Get("Set-Cookie"), "; Max-Age=600;", "the cookie lasts until the refreshed idle deadline")
	require.Equal(t, 2.0, count(t, ResultHit))
}

func TestMarkPrivate(t *testing.T) {
	p := persistence(t, "cookie: {mark_private: true}")
	h := http.Header{"Cache-Control": {"public, max-age=60"}}
	var s Session
	p.Begin(request(nil), &s, clock)
	s.Chosen = leaf
	p.Finish(h, request(nil), &s)
	require.Equal(t, []string{"public, max-age=60", "private"}, h.Values("Cache-Control"))
	// a response no shared cache would store is left as it is
	for _, cc := range []string{"private", "no-store"} {
		h := http.Header{"Cache-Control": {cc}}
		p.Begin(request(nil), &s, clock)
		s.Chosen = leaf
		p.Finish(h, request(nil), &s)
		require.Equal(t, []string{cc}, h.Values("Cache-Control"))
	}
	// and nothing is marked when no cookie is set
	unmarked := persistence(t, "{}")
	h, _ = serve(unmarked, request(nil), clock, leaf)
	require.Empty(t, h.Values("Cache-Control"))
}

func TestHeaderMode(t *testing.T) {
	p := persistence(t, "mode: header\nheader: {name: x-session}\n")
	h, _ := serve(p, request(nil), clock, leaf)
	tok := h.Get("X-Session")
	require.NotEmpty(t, tok)
	require.Empty(t, h.Get("Set-Cookie"))
	h, s := serve(p, request(func(r *http.Request) { r.Header.Set("X-Session", tok) }), clock, leaf)
	require.Equal(t, leaf, s.Pins)
	require.Empty(t, h)
	// a cookie of the same name is not where header mode reads its token
	_, s = serve(p, request(withCookie("X-Session", tok)), clock, leaf)
	require.Zero(t, s.Pins.Depth)
}

func TestRefusedAndUnsentSessionsAreLeftAlone(t *testing.T) {
	p := persistence(t, "on_unavailable: reject")
	var s Session
	p.Begin(request(nil), &s, clock)
	require.True(t, s.Rejects())
	s.Record(0, leaf.Hashes[0])
	s.Reject()
	h := http.Header{}
	p.Finish(h, request(nil), &s)
	require.Empty(t, h, "a refused request was issued a token")
	require.Equal(t, 1.0, count(t, ResultRejected))
	// a request sent to no member has nothing to pin
	h, _ = serve(p, request(nil), clock, Path{})
	require.Empty(t, h)
	require.False(t, (&Session{}).Rejects())
}

func TestTableMode(t *testing.T) {
	p := persistence(t, "mode: table\ntable: {key: header:X-Client}\n")
	client := func(r *http.Request) { r.Header.Set("X-Client", "alice") }
	h, s := serve(p, request(client), clock, leaf)
	require.Empty(t, h, "table mode issues nothing to the client")
	require.Zero(t, s.Pins.Depth)
	require.Equal(t, 1, p.table.Len())
	_, s = serve(p, request(client), clock.Add(time.Minute), leaf)
	require.Equal(t, leaf, s.Pins)
	require.Equal(t, 1.0, count(t, ResultHit))
	// a move re-pins the key; a request without one keeps nothing
	_, _ = serve(p, request(client), clock.Add(time.Minute), other)
	_, s = serve(p, request(client), clock.Add(2*time.Minute), other)
	require.Equal(t, other, s.Pins)
	_, _ = serve(p, request(nil), clock, leaf)
	require.Equal(t, 1, p.table.Len())
	require.Equal(t, 1.0, count(t, ResultRepick))
	require.Equal(t, 2.0, count(t, ResultMiss))
	// past the ttl the entry is gone, which is a miss, as no token says it expired
	_, s = serve(p, request(client), clock.Add(3*time.Hour), leaf)
	require.Zero(t, s.Pins.Depth)
	require.Zero(t, count(t, ResultExpired))
}

// an upstream that hands each client a session id in its response keeps every follow-up that
// carries the id on the member that minted it
func TestTableLearnsFromResponses(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		set  func(http.Header, string)
		send func(string) func(*http.Request)
	}{
		"header": {
			doc: "mode: table\ntable: {key: header:Mcp-Session-Id, learn: response}\n",
			set: func(h http.Header, id string) { h.Set("Mcp-Session-Id", id) },
			send: func(id string) func(*http.Request) {
				return func(r *http.Request) { r.Header.Set("Mcp-Session-Id", id) }
			},
		},
		"cookie": {
			doc:  "mode: table\ntable: {key: cookie:JSESSIONID, learn: response}\n",
			set:  func(h http.Header, id string) { h.Add("Set-Cookie", "JSESSIONID="+id+"; Path=/; HttpOnly") },
			send: func(id string) func(*http.Request) { return withCookie("JSESSIONID", id) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := persistence(t, tc.doc)
			finish := func(r *http.Request, chosen Path, respond string) *Session {
				var s Session
				p.Begin(r, &s, clock)
				s.Chosen = chosen
				h := http.Header{}
				if respond != "" {
					tc.set(h, respond)
				}
				p.Finish(h, r, &s)
				return &s
			}
			// the first request carries no id; the member's response mints one
			finish(request(nil), leaf, "s-1")
			require.Equal(t, leaf, finish(request(tc.send("s-1")), leaf, "").Pins)
			// a response that sets the value it was sent stores nothing new
			finish(request(tc.send("s-1")), leaf, "s-1")
			require.Equal(t, 1, p.table.Len())
			// a response that sets another value re-pins the client by the new one
			finish(request(tc.send("s-1")), other, "s-2")
			require.Equal(t, other, finish(request(tc.send("s-2")), other, "").Pins)
			require.Equal(t, 2, p.table.Len(), "the request's own id, re-pinned, and the new one")
		})
	}
}

func TestSessionLevels(t *testing.T) {
	s := &Session{Pins: nested}
	pin, ok := s.Pin(0)
	require.True(t, ok)
	require.Equal(t, nested.Hashes[0], pin)
	_, ok = s.Pin(1)
	require.False(t, ok, "the next level is pinned only once the first was picked")
	s.Record(0, nested.Hashes[0])
	pin, ok = s.Pin(1)
	require.True(t, ok)
	require.Equal(t, nested.Hashes[1], pin)
	s.Record(1, nested.Hashes[1])
	require.Equal(t, nested, s.Chosen)
	_, ok = s.Pin(2)
	require.False(t, ok)
	// a session moved at the first level starts afresh below it
	moved := &Session{Pins: nested}
	moved.Record(0, 0x9999)
	_, ok = moved.Pin(1)
	require.False(t, ok)
	moved.Record(lb.MaxPickDepth, 1)
	moved.Record(-1, 1)
	require.Equal(t, uint8(1), moved.Chosen.Depth, "a level outside the path is ignored")
}

func TestNestedSessionIsClaimedOnceByItsMember(t *testing.T) {
	p := persistence(t, "{}")
	var s Session
	p.Begin(request(nil), &s, clock)
	s.Record(0, 7)
	require.Nil(t, SessionFrom(context.Background()))
	ctx, carried := Nest(context.Background(), &s, "inner")
	require.Same(t, carried, SessionFrom(ctx))
	require.Equal(t, s.Chosen, carried.Chosen)
	require.False(t, carried.Claim("outer"), "a session was claimed by an ALB it was not sent to")
	require.True(t, carried.Claim("inner"))
	require.False(t, carried.Claim("inner"), "a session's next level was claimed twice")
	require.False(t, s.Claim(""), "a session no ALB was sent was claimed")
}

func TestEntriesGauge(t *testing.T) {
	p := persistence(t, "mode: table\n")
	_, _ = serve(p, request(nil), clock, leaf)
	want := `
# HELP trickster_alb_sticky_entries Current number of pins an ALB keeps in its sticky table, expired ones not yet removed included.
# TYPE trickster_alb_sticky_entries gauge
trickster_alb_sticky_entries{alb_name="TestEntriesGauge"} 1
`
	require.NoError(t, testutil.CollectAndCompare(entriesCollector{}, strings.NewReader(want)))
}

func TestTableIsTheOneKept(t *testing.T) {
	table := persistence(t, "mode: table\n")
	require.NotNil(t, table.Table())
	require.Nil(t, persistence(t, "{}").Table(), "a token mode keeps a table")
	require.Nil(t, (*HTTP)(nil).Table())
}

// a protocol switch settles the session when its connection is taken, and learns from its response
// only once that response has the upstream's headers
func TestFinishSwitch(t *testing.T) {
	cookie := persistence(t, "{}")
	var s Session
	h := http.Header{}
	cookie.Begin(request(nil), &s, clock)
	s.Chosen = leaf
	require.Nil(t, cookie.FinishSwitch(h, request(nil), &s), "a token mode has nothing to learn")
	require.NotEmpty(t, h.Get("Set-Cookie"), "the switch's token was not issued")

	learns := persistence(t, "mode: table\ntable: {key: header:X-Session, learn: response}\n")
	h = http.Header{}
	learns.Begin(request(nil), &s, clock)
	s.Chosen = leaf
	learn := learns.FinishSwitch(h, request(nil), &s)
	require.NotNil(t, learn)
	// the session's writer may be reused before the response is written
	learns.Begin(request(nil), &s, clock)
	h.Set("X-Session", "s-1")
	learn()
	_, pinned := serve(learns, request(func(r *http.Request) { r.Header.Set("X-Session", "s-1") }), clock, leaf)
	require.Equal(t, leaf, pinned.Pins)

	s.Reject()
	require.Nil(t, learns.FinishSwitch(http.Header{}, request(nil), &s), "a refused switch learned")
}
