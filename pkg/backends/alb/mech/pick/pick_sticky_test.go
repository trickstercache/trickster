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
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky"
	so "github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/lb"
	"github.com/trickstercache/trickster/v2/pkg/lb/lc"
	"github.com/trickstercache/trickster/v2/pkg/lb/lt"
	"github.com/trickstercache/trickster/v2/pkg/lb/rr"
	"github.com/trickstercache/trickster/v2/pkg/secret"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const cookieName = so.DefaultCookieName

// member is a named pool member, which a session can be pinned to
type member struct {
	name   string
	target *pool.Target
	status *healthcheck.Status
}

func named(t testing.TB, name string, h http.Handler) member {
	t.Helper()
	return namedBackend(t, name, h, nil)
}

// namedBackend is named with the backend wrapped by wrap, such as one that is itself an ALB
func namedBackend(t testing.TB, name string, h http.Handler, wrap func(backends.Backend) backends.Backend) member {
	t.Helper()
	o := bo.New()
	require.NoError(t, o.Initialize(name))
	b, err := backends.New(name, o, nil, h, nil)
	require.NoError(t, err)
	if wrap != nil {
		b = wrap(b)
	}
	status := healthcheck.NewStatus(name, "", "", healthcheck.StatusPassing, time.Time{}, nil)
	return member{name: name, target: pool.NewTarget(h, status, b), status: status}
}

func poolOf(t testing.TB, members ...member) pool.Pool {
	t.Helper()
	targets := make(pool.Targets, len(members))
	for i, m := range members {
		targets[i] = m.target
	}
	p := pool.New(targets, 0)
	t.Cleanup(p.Stop)
	return p
}

// persistence is the http persistence a sticky block configures on an ALB named for the test
func persistence(t testing.TB, doc string) *sticky.HTTP {
	t.Helper()
	o := &so.Options{}
	require.NoError(t, yaml.Unmarshal([]byte(doc), o))
	o.Secret = secret.Secret(strings.Repeat("k", secret.MinKeyBytes))
	require.NoError(t, o.Initialize())
	name := t.Name()
	t.Cleanup(func() { sticky.ForgetTablesExcept(func(n string, _ *sticky.Table) bool { return n != name }) })
	p, err := sticky.NewHTTP(name, o)
	require.NoError(t, err)
	return p
}

func stickyALB(t testing.TB, doc string, selector lb.Selector, members ...member) *handler {
	t.Helper()
	h := New(names.MechanismRR, selector, Options{Sticky: persistence(t, doc), ALBName: t.Name()}).(*handler)
	h.SetPool(poolOf(t, members...))
	return h
}

// client sends requests with whatever token or cookie the ALB last issued it
type client struct {
	t      testing.TB
	h      http.Handler
	cookie string
	issued int
}

func (c *client) get(mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	if c.cookie != "" {
		r.Header.Set("Cookie", c.cookie)
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	if line := w.Header().Get("Set-Cookie"); line != "" {
		c.cookie, _, _ = strings.Cut(line, ";")
		c.issued++
	}
	return w
}

func TestStickyDispatchIssuesAndHonors(t *testing.T) {
	a, b := named(t, "a", albpool.NamedHandler("a")), named(t, "b", albpool.NamedHandler("b"))
	c := &client{t: t, h: stickyALB(t, "{}", rr.New(), a, b)}
	first := c.get(nil).Body.String()
	require.Equal(t, 1, c.issued)
	require.True(t, strings.HasPrefix(c.cookie, cookieName+"="))
	// round robin would alternate; the session keeps every request on its member, quietly
	for range 6 {
		require.Equal(t, first, c.get(nil).Body.String())
	}
	require.Equal(t, 1, c.issued, "a request served by its pinned member was issued a token")
	// a client without the cookie is a new session
	fresh := &client{t: t, h: c.h}
	fresh.get(nil)
	require.Equal(t, 1, fresh.issued)
}

func TestStickyDispatchMovesOffAnUnavailableMember(t *testing.T) {
	a, b := named(t, "a", albpool.NamedHandler("a")), named(t, "b", albpool.NamedHandler("b"))
	c := &client{t: t, h: stickyALB(t, "{}", rr.New(), a, b)}
	pinned := map[string]member{"a": a, "b": b}[c.get(nil).Body.String()]
	pinned.status.Set(healthcheck.StatusFailing)
	moved := c.get(nil).Body.String()
	require.NotEqual(t, pinned.name, moved)
	require.Equal(t, 2, c.issued, "a moved session was not issued a token for its new member")
	// the session stays where it moved, even once its first member returns
	pinned.status.Set(healthcheck.StatusPassing)
	for range 4 {
		require.Equal(t, moved, c.get(nil).Body.String())
	}
	require.Equal(t, 2, c.issued)
}

func TestStickyDispatchRejects(t *testing.T) {
	a, b := named(t, "a", albpool.NamedHandler("a")), named(t, "b", albpool.NamedHandler("b"))
	h := stickyALB(t, "on_unavailable: reject", lc.New(), a, b)
	c := &client{t: t, h: h}
	pinned := map[string]member{"a": a, "b": b}[c.get(nil).Body.String()]
	pinned.status.Set(healthcheck.StatusFailing)
	w := c.get(nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Empty(t, w.Header().Get("Set-Cookie"), "a refused request was issued a token")
	for _, m := range []member{a, b} {
		require.Zero(t, m.target.Member().Stats().Inflight(), "the refused pick of %s was not undone", m.name)
	}
	// with no member eligible at all, the pinned one is still unavailable rather than gone
	other := map[string]member{"a": b, "b": a}[pinned.name]
	other.status.Set(healthcheck.StatusFailing)
	require.Equal(t, http.StatusServiceUnavailable, c.get(nil).Code)
	other.status.Set(healthcheck.StatusPassing)
	// the session returns to its member when it does
	pinned.status.Set(healthcheck.StatusPassing)
	require.Equal(t, pinned.name, c.get(nil).Body.String())
	require.Equal(t, 1, c.issued)
	// a member that has left the pool has no session to keep, so the request starts a new one
	h.SetPool(poolOf(t, other))
	w = c.get(nil)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, other.name, w.Body.String())
	require.Equal(t, 2, c.issued)
	// and a request with no member to go to is a gateway failure, as ever
	other.status.Set(healthcheck.StatusFailing)
	require.Equal(t, http.StatusBadGateway, (&client{t: t, h: h}).get(nil).Code)
}

func TestStickyDispatchIssuesOnAnEmptyAnswer(t *testing.T) {
	silent := named(t, "silent", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := &client{t: t, h: stickyALB(t, "{}", rr.New(), silent)}
	c.get(nil)
	require.Equal(t, 1, c.issued, "a member that wrote nothing left its session without a token")
}

// an informational response goes out before the member's answer, and must not carry the token
func TestStickyTokenIsNotSentWithAnInformationalResponse(t *testing.T) {
	hinting := named(t, "hinting", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "</app.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		_, _ = w.Write([]byte("hinting"))
	}))
	srv := httptest.NewServer(stickyALB(t, "{}", rr.New(), hinting))
	defer srv.Close()
	var mu sync.Mutex
	var early []textproto.MIMEHeader
	trace := &httptrace.ClientTrace{Got1xxResponse: func(_ int, h textproto.MIMEHeader) error {
		mu.Lock()
		early = append(early, h)
		mu.Unlock()
		return nil
	}}
	r, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), trace), http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(r)
	require.NoError(t, err)
	resp.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, early, 1)
	require.Empty(t, early[0].Get("Set-Cookie"))
	require.NotEmpty(t, resp.Header.Get("Set-Cookie"))
}

// innerALB is a pool member backed by an ALB of its own, which a session's path continues into
type innerALB struct {
	backends.Backend
	picker lb.Picker
}

func (i innerALB) Picker() lb.Picker { return i.picker }

func nestedMember(t testing.TB, name string, inner *handler) member {
	return namedBackend(t, name, inner, func(b backends.Backend) backends.Backend {
		return innerALB{Backend: b, picker: inner.Picker()}
	})
}

func TestStickyPathCarriesIntoNestedALBs(t *testing.T) {
	leaves := map[string]member{}
	inner := func(name string) *handler {
		a := named(t, name+"-a", albpool.NamedHandler(name+"-a"))
		b := named(t, name+"-b", albpool.NamedHandler(name+"-b"))
		leaves[a.name], leaves[b.name] = a, b
		h := New(names.MechanismRR, rr.New(), Options{ALBName: name}).(*handler)
		h.SetPool(poolOf(t, a, b))
		h.FollowPins()
		return h
	}
	east, west := inner("east"), inner("west")
	c := &client{t: t, h: stickyALB(t, "{}", rr.New(), nestedMember(t, "east", east), nestedMember(t, "west", west))}
	leaf := c.get(nil).Body.String()
	// both levels would rotate on their own; the one token keeps the whole path
	for range 6 {
		require.Equal(t, leaf, c.get(nil).Body.String())
	}
	require.Equal(t, 1, c.issued)
	// a leaf that fails moves only the inner level, and the new token names the new path
	region, _, _ := strings.Cut(leaf, "-")
	leaves[leaf].status.Set(healthcheck.StatusFailing)
	moved := c.get(nil).Body.String()
	require.NotEqual(t, leaf, moved)
	require.True(t, strings.HasPrefix(moved, region+"-"), "the session left its region for %s", moved)
	require.Equal(t, 2, c.issued)
	leaves[leaf].status.Set(healthcheck.StatusPassing)
	for range 4 {
		require.Equal(t, moved, c.get(nil).Body.String())
	}
	// an inner ALB reached without the outer one's session picks on its own
	direct := httptest.NewRecorder()
	east.ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	require.Equal(t, http.StatusOK, direct.Code)
	require.Empty(t, direct.Header().Get("Set-Cookie"))
}

func TestNestedStickyALBFollowsTheOuterSession(t *testing.T) {
	a := named(t, "a", albpool.NamedHandler("a"))
	b := named(t, "b", albpool.NamedHandler("b"))
	// an inner ALB that keeps sessions of its own leaves them to the outer ALB it is reached by
	inner := New(names.MechanismRR, rr.New(), Options{
		ALBName: "inner", Sticky: persistence(t, "cookie: {name: inner}"),
	}).(*handler)
	inner.SetPool(poolOf(t, a, b))
	inner.FollowPins()
	c := &client{t: t, h: stickyALB(t, "{}", rr.New(), nestedMember(t, "inner", inner))}
	leaf := c.get(nil).Body.String()
	for range 4 {
		require.Equal(t, leaf, c.get(nil).Body.String())
	}
	require.Equal(t, 1, c.issued)
	require.True(t, strings.HasPrefix(c.cookie, cookieName+"="), "the inner ALB issued its own token")
	// reached directly it keeps its own sessions
	direct := &client{t: t, h: inner}
	direct.get(nil)
	require.True(t, strings.HasPrefix(direct.cookie, "inner="))
}

func TestStickyRejectsAtTheNestedLevel(t *testing.T) {
	a := named(t, "a", albpool.NamedHandler("a"))
	b := named(t, "b", albpool.NamedHandler("b"))
	inner := New(names.MechanismRR, rr.New(), Options{ALBName: "inner"}).(*handler)
	inner.SetPool(poolOf(t, a, b))
	inner.FollowPins()
	c := &client{t: t, h: stickyALB(t, "on_unavailable: reject", rr.New(), nestedMember(t, "inner", inner))}
	pinned := map[string]member{"a": a, "b": b}[c.get(nil).Body.String()]
	pinned.status.Set(healthcheck.StatusFailing)
	w := c.get(nil)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Empty(t, w.Header().Get("Set-Cookie"))
}

// the inner level keeps its own strategy's accounting while it follows the outer ALB's pins
func TestStickyNestedLevelKeepsItsAccounting(t *testing.T) {
	for name, selector := range map[string]lb.Selector{"tracked": lc.New(), "timed": lt.New(lt.Options{})} {
		t.Run(name, func(t *testing.T) {
			a := named(t, "a", albpool.NamedHandler("a"))
			b := named(t, "b", albpool.NamedHandler("b"))
			inner := New(names.MechanismLC, selector, Options{ALBName: "inner"}).(*handler)
			inner.SetPool(poolOf(t, a, b))
			inner.FollowPins()
			c := &client{t: t, h: stickyALB(t, "{}", rr.New(), nestedMember(t, "inner", inner))}
			leaf := c.get(nil).Body.String()
			for range 4 {
				require.Equal(t, leaf, c.get(nil).Body.String())
			}
			for _, m := range []member{a, b} {
				require.Zero(t, m.target.Member().Stats().Inflight(), "%s leaked an in-flight count", m.name)
			}
		})
	}
}

// a member with nothing to dispatch to is a gateway failure, and its pick is reported done
func TestStickyDispatchToAMemberWithoutAHandler(t *testing.T) {
	empty := named(t, "empty", nil)
	h := stickyALB(t, "{}", lc.New(), empty)
	w := (&client{t: t, h: h}).get(nil)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Empty(t, w.Header().Get("Set-Cookie"))
	require.Zero(t, empty.target.Member().Stats().Inflight())
}

func TestStickyHeaderMode(t *testing.T) {
	a, b := named(t, "a", albpool.NamedHandler("a")), named(t, "b", albpool.NamedHandler("b"))
	h := stickyALB(t, "mode: header", rr.New(), a, b)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	tok := w.Header().Get(so.DefaultHeaderName)
	require.NotEmpty(t, tok)
	for range 4 {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		r.Header.Set(so.DefaultHeaderName, tok)
		again := httptest.NewRecorder()
		h.ServeHTTP(again, r)
		require.Equal(t, w.Body.String(), again.Body.String())
		require.Empty(t, again.Header().Get(so.DefaultHeaderName))
	}
}

// minting answers a request without a session id by minting one, and one with an id it did not
// mint with 404, as an MCP server does; set hands the id to the client
func minting(name string, set func(http.Header, string), read func(*http.Request) string) (
	build func(testing.TB) member, reissue func(),
) {
	var mu sync.Mutex
	minted := map[string]bool{}
	n := 0
	fresh := false
	return func(t testing.TB) member {
			return named(t, name, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				id := read(r)
				if id != "" && !minted[id] {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if id == "" || fresh {
					fresh = false
					n++
					id = fmt.Sprintf("%s-%d", name, n)
					minted[id] = true
					set(w.Header(), id)
				}
				_, _ = w.Write([]byte(id))
			}))
		}, func() {
			mu.Lock()
			fresh = true
			mu.Unlock()
		}
}

// every follow-up that carries a session id an upstream minted in its response reaches the
// member that minted it, and a response that sets a new id moves the key to it
func TestStickyTableLearnsSessionsFromResponses(t *testing.T) {
	for form, tc := range map[string]struct {
		doc  string
		set  func(http.Header, string)
		read func(*http.Request) string
		send func(*http.Request, string)
	}{
		"header": {
			doc:  "mode: table\ntable: {key: header:Mcp-Session-Id, learn: response}\n",
			set:  func(h http.Header, id string) { h.Set("Mcp-Session-Id", id) },
			read: func(r *http.Request) string { return r.Header.Get("Mcp-Session-Id") },
			send: func(r *http.Request, id string) { r.Header.Set("Mcp-Session-Id", id) },
		},
		"cookie": {
			doc: "mode: table\ntable: {key: cookie:SID, learn: response}\n",
			set: func(h http.Header, id string) { h.Add("Set-Cookie", "SID="+id+"; Path=/") },
			read: func(r *http.Request) string {
				c, err := r.Cookie("SID")
				if err != nil {
					return ""
				}
				return c.Value
			},
			send: func(r *http.Request, id string) { r.AddCookie(&http.Cookie{Name: "SID", Value: id}) },
		},
	} {
		t.Run(form, func(t *testing.T) {
			newA, reissueA := minting("a", tc.set, tc.read)
			newB, _ := minting("b", tc.set, tc.read)
			newC, _ := minting("c", tc.set, tc.read)
			h := stickyALB(t, tc.doc, rr.New(), newA(t), newB(t), newC(t))
			call := func(id string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "http://example.com/mcp", nil)
				if id != "" {
					tc.send(r, id)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			var ids []string
			for range 6 {
				w := call("")
				require.Equal(t, http.StatusOK, w.Code)
				ids = append(ids, w.Body.String())
			}
			for range 3 {
				for _, id := range ids {
					w := call(id)
					require.Equal(t, http.StatusOK, w.Code, "session %s reached a member that never minted it", id)
					require.Equal(t, id, w.Body.String())
				}
			}
			// a member that re-sets the id keeps the client on it by the new one
			var fromA string
			for _, id := range ids {
				if strings.HasPrefix(id, "a-") {
					fromA = id
					break
				}
			}
			require.NotEmpty(t, fromA)
			reissueA()
			renewed := call(fromA).Body.String()
			require.NotEqual(t, fromA, renewed)
			for range 3 {
				w := call(renewed)
				require.Equal(t, http.StatusOK, w.Code)
				require.Equal(t, renewed, w.Body.String())
			}
		})
	}
}

func TestStickyTableOfTheMechanism(t *testing.T) {
	a := named(t, "a", albpool.NamedHandler("a"))
	require.NotNil(t, stickyALB(t, "mode: table", rr.New(), a).StickyTable())
	require.Nil(t, stickyALB(t, "{}", rr.New(), a).StickyTable())
	require.Nil(t, New(names.MechanismRR, rr.New()).(*handler).StickyTable())
}

func TestFollowingALBWithoutASessionPicksOnItsOwn(t *testing.T) {
	a := named(t, "a", albpool.NamedHandler("a"))
	h := New(names.MechanismRR, rr.New(), Options{ALBName: "inner"}).(*handler)
	h.SetPool(poolOf(t, a))
	h.FollowPins()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	require.Equal(t, "a", w.Body.String())
	// a nil request is still answered, as it was before sessions
	w = httptest.NewRecorder()
	h.ServeHTTP(w, nil)
	require.Equal(t, http.StatusOK, w.Code)
}

func BenchmarkStickyDispatch(b *testing.B) {
	nop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	members := func(b *testing.B) []member {
		return []member{named(b, "a", nop), named(b, "b", nop)}
	}
	plain := New(names.MechanismRR, rr.New()).(*handler)
	plain.SetPool(poolOf(b, members(b)...))
	cookie := stickyALB(b, "{}", rr.New(), members(b)...)
	c := &client{t: b, h: cookie}
	c.get(nil)
	table := stickyALB(b, "mode: table", rr.New(), members(b)...)
	(&client{t: b, h: table}).get(nil)
	for name, tc := range map[string]struct {
		h      http.Handler
		cookie string
	}{
		"unsticky":     {plain, ""},
		"cookie hit":   {cookie, c.cookie},
		"cookie issue": {cookie, ""},
		"table hit":    {table, ""},
	} {
		b.Run(name, func(b *testing.B) {
			r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
			if tc.cookie != "" {
				r.Header.Set("Cookie", tc.cookie)
			}
			w := httptest.NewRecorder()
			b.ReportAllocs()
			for b.Loop() {
				clear(w.HeaderMap)
				tc.h.ServeHTTP(w, r)
			}
		})
	}
}
