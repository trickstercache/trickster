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
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/sticky/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/cachecontrol"
	"github.com/trickstercache/trickster/v2/pkg/proxy/flowkey"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/prometheus/client_golang/prometheus"
)

// How a request's or flow's session fared, as the sticky metric counts it
const (
	// ResultHit is a session sent down the path it was pinned to.
	ResultHit = "hit"
	// ResultMiss is a request with no token, or no table entry for its key.
	ResultMiss = "miss"
	// ResultExpired is a token past its ttl or idle timeout; a table entry past them is a miss.
	ResultExpired = "expired"
	// ResultInvalid is a token that is malformed, altered, keyed with another key or another ALB's.
	ResultInvalid = "invalid"
	// ResultRepick is a session whose pinned member was unavailable, which was moved.
	ResultRepick = "repick"
	// ResultRejected is a session whose pinned member was unavailable, which was refused.
	ResultRejected = "rejected"
)

type result uint8

const (
	resultHit result = iota
	resultMiss
	resultExpired
	resultInvalid
	resultRepick
	resultRejected
	resultCount
)

var resultNames = [resultCount]string{
	ResultHit, ResultMiss, ResultExpired, ResultInvalid, ResultRepick, ResultRejected,
}

// what the lookup of a request's pins found
type found uint8

const (
	foundNothing found = iota
	foundInvalid
	foundExpired
	foundPins
)

// The cookie attributes a token is issued with
const (
	attrPath           = "; Path="
	attrDomain         = "; Domain="
	attrMaxAge         = "; Max-Age="
	attrHTTPOnly       = "; HttpOnly"
	attrSecure         = "; Secure"
	attrSameSiteLax    = "; SameSite=Lax"
	attrSameSiteStrict = "; SameSite=Strict"
	attrSameSiteNone   = "; SameSite=None"
)

// ErrNotInitialized is returned for sticky options that were never initialized, which have no
// key to make tokens with.
var ErrNotInitialized = errors.New("sticky options are not initialized")

// HTTP keeps an ALB's sessions on http listeners: it reads a request's pins before the pick, and
// issues its token or stores its pins as the response begins. It is safe for concurrent use.
type HTTP struct {
	mode  string
	codec *Codec
	table *Table
	// name is the cookie's, or the header's in its canonical form
	name string
	// attrs is a cookie's attributes after its value and Max-Age: without Secure, then with it
	attrs       [2]string
	secure      string
	maxAge      bool
	markPrivate bool
	key         func(*http.Request) flowkey.Value
	learned     func(http.Header) flowkey.Value
	reject      bool
	results     [resultCount]prometheus.Counter
}

// NewHTTP returns the persistence that initialized options configure on the named ALB's http
// listeners, in the mode in effect there.
func NewHTTP(albName string, o *options.Options) (*HTTP, error) {
	if o == nil || o.Keys() == nil {
		return nil, ErrNotInitialized
	}
	p := &HTTP{mode: o.ModeFor(true), reject: o.OnUnavailable == options.OnUnavailableReject}
	for i := range p.results {
		p.results[i] = metrics.ALBStickyResults.WithLabelValues(albName, resultNames[i])
	}
	switch p.mode {
	case options.ModeTable:
		p.table = TableFor(albName, o)
		p.key = flowkey.HTTP(o.Table.KeySource, o.Table.IPv6Prefix)
		if o.Table.Learn == options.LearnResponse {
			p.learned = flowkey.HTTPResponse(o.Table.KeySource)
		}
		return p, nil
	case options.ModeHeader:
		p.name = o.Header.Name
	default:
		p.name, p.secure, p.markPrivate = o.Cookie.Name, o.Cookie.Secure, o.Cookie.MarkPrivate
		// a ttl of 0 keeps the cookie for the browser's session, whatever the idle timeout
		p.maxAge = o.TTLDuration() > 0 && o.Cookie.Lifetime != options.LifetimeSession
		p.attrs = cookieAttributes(o.Cookie)
	}
	c, err := NewCodec(o.Keys(), albName, o.TTLDuration(), time.Duration(o.Idle))
	if err != nil {
		return nil, err
	}
	p.codec = c
	return p, nil
}

// Table returns the table the persistence keeps its pins in, or nil when it keeps none.
func (p *HTTP) Table() *Table {
	if p == nil {
		return nil
	}
	return p.table
}

func cookieAttributes(c options.CookieOptions) [2]string {
	var b strings.Builder
	if c.Path != "" {
		b.WriteString(attrPath + c.Path)
	}
	if c.Domain != "" {
		b.WriteString(attrDomain + c.Domain)
	}
	if c.HTTPOnly == nil || *c.HTTPOnly {
		b.WriteString(attrHTTPOnly)
	}
	switch c.SameSite {
	case options.SameSiteStrict:
		b.WriteString(attrSameSiteStrict)
	case options.SameSiteNone:
		b.WriteString(attrSameSiteNone)
	default:
		b.WriteString(attrSameSiteLax)
	}
	plain := b.String()
	return [2]string{plain, plain + attrSecure}
}

// Session is one request's passage through the persistence of the outermost ALB on its way that
// keeps sessions: the path its token or table entry pins it to, and the path it is sent down.
type Session struct {
	passage
	owner *HTTP
	now   time.Time
	token Token
	// via is the pool member that the owner sent the request to: an ALB, which picks the next level
	via     string
	claimed atomic.Bool
}

// Begin resets the session for the request and reads its pins, as of now.
func (p *HTTP) Begin(r *http.Request, s *Session, now time.Time) {
	s.passage, s.owner, s.now, s.token, s.via = passage{}, p, now, Token{}, ""
	s.claimed.Store(false)
	if p.table != nil {
		if s.key = p.key(r); s.key.OK {
			if path, ok := p.table.Get(s.key.Hash, now.UnixNano()); ok {
				s.Pins, s.found = path, foundPins
			}
		}
		return
	}
	var v string
	var ok bool
	if p.mode == options.ModeHeader {
		if vs := r.Header[p.name]; len(vs) > 0 {
			v, ok = vs[0], true
		}
	} else {
		v, ok = flowkey.Cookie(r.Header, p.name)
	}
	if !ok {
		return
	}
	switch tok, st := p.codec.Read(v, now.Unix()); st {
	case Valid:
		s.token, s.Pins, s.found = tok, tok.Path, foundPins
	case Expired:
		s.found = foundExpired
	default:
		s.found = foundInvalid
	}
}

// Finish counts how the session fared and, unless it was refused or sent nowhere, issues or
// refreshes its token, or stores its pins, in the response headers h while they can still change.
func (p *HTTP) Finish(h http.Header, r *http.Request, s *Session) {
	if p.settle(h, r, s) && p.learned != nil {
		p.learn(h, s.key, s.Chosen, s.now.UnixNano())
	}
}

// FinishSwitch is Finish for a protocol switch, whose response the handler that took over the
// connection writes itself from h, once it has added the upstream's headers. It returns what
// learns the value that response sets, to run as it is written, or nil when there is none to learn.
func (p *HTTP) FinishSwitch(h http.Header, r *http.Request, s *Session) func() {
	if !p.settle(h, r, s) || p.learned == nil {
		return nil
	}
	// the session may belong to a pooled writer by the time the response is written
	key, chosen, nowNano := s.key, s.Chosen, s.now.UnixNano()
	return func() { p.learn(h, key, chosen, nowNano) }
}

// settle counts the result and issues the session's token or pins its request's key, and reports
// whether the session was sent to a member
func (p *HTTP) settle(h http.Header, r *http.Request, s *Session) bool {
	if !s.rejected && !s.reached() {
		// the ALB answered a request that no member took, which counts toward no result
		return false
	}
	p.results[s.result()].Inc()
	if s.rejected {
		return false
	}
	if p.table == nil {
		p.issue(h, r, s)
		return true
	}
	if s.key.OK && (s.found != foundPins || s.Chosen != s.Pins) {
		p.table.Put(s.key.Hash, s.Chosen, s.now.UnixNano())
	}
	return true
}

// learn pins the value a response hands the client, when the ALB learns from responses; the
// request's own value is pinned already, and a different one replaces it
func (p *HTTP) learn(h http.Header, key flowkey.Value, chosen Path, nowNano int64) {
	if v := p.learned(h); v.OK && v != key {
		p.table.Put(v.Hash, chosen, nowNano)
	}
}

// issue sets a token for a new or moved session, and for one whose idle refresh is due; a
// session sent down the path its token names otherwise gets none
func (p *HTTP) issue(h http.Header, r *http.Request, s *Session) {
	nowSec := s.now.Unix()
	t := Token{Path: s.Chosen, Born: nowSec, Issued: nowSec}
	if s.found == foundPins && s.Chosen == s.Pins {
		if !p.codec.RefreshDue(s.token, nowSec) {
			return
		}
		t.Born = s.token.Born
	}
	v := p.codec.Mint(t)
	if p.mode == options.ModeHeader {
		h.Set(p.name, v)
		return
	}
	h.Add(headers.NameSetCookie, p.cookie(v, t, nowSec, r))
	if p.markPrivate {
		markPrivate(h)
	}
}

// maxDigits is the most digits a Max-Age can have
const maxDigits = 20

func (p *HTTP) cookie(v string, t Token, nowSec int64, r *http.Request) string {
	attrs := p.attrs[0]
	if p.secure == options.SecureAlways || (p.secure == options.SecureAuto && r != nil && r.TLS != nil) {
		attrs = p.attrs[1]
	}
	var b strings.Builder
	b.Grow(len(p.name) + 1 + len(v) + len(attrMaxAge) + maxDigits + len(attrs))
	b.WriteString(p.name)
	b.WriteByte('=')
	b.WriteString(v)
	if exp := p.codec.Expires(t); p.maxAge && exp > 0 {
		var digits [maxDigits]byte
		b.WriteString(attrMaxAge)
		b.Write(strconv.AppendInt(digits[:0], max(exp-nowSec, 1), 10))
	}
	b.WriteString(attrs)
	return b.String()
}

// markPrivate keeps a shared cache from storing a response that sets a cookie
func markPrivate(h http.Header) {
	if d := cachecontrol.ParseResponse(h); d.Private || d.NoStore {
		return
	}
	h.Add(headers.NameCacheControl, headers.ValuePrivate)
}

// Rejects reports whether a request whose pinned member is unavailable is refused, not moved.
func (s *Session) Rejects() bool {
	return s.owner != nil && s.owner.reject
}

// Reject notes that the request was refused, so that its token is kept and nothing is stored.
func (s *Session) Reject() {
	s.rejected = true
}

type sessionKey struct{}

// Nest returns a context carrying a copy of the session to the pool member named via, an ALB
// that picks the session's next level, and the copy, which the caller uses from then on.
func Nest(ctx context.Context, s *Session, via string) (context.Context, *Session) {
	c := &Session{passage: s.passage, owner: s.owner, now: s.now, token: s.token, via: via}
	return context.WithValue(ctx, sessionKey{}, c), c
}

// SessionFrom returns the session a request's context carries, or nil.
func SessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey{}).(*Session)
	return s
}

// Claim reports whether the named ALB is the pool member the session was sent to, the first time
// that it asks: only that ALB, and only once, picks the session's next level.
func (s *Session) Claim(albName string) bool {
	return s.via != "" && s.via == albName && s.claimed.CompareAndSwap(false, true)
}

// reached reports whether the request reached a member: one was chosen at the first level, and an
// ALB there that claimed the session chose one at the next
func (s *Session) reached() bool {
	return s.Chosen.Depth > 0 && (s.Chosen.Depth > 1 || !s.claimed.Load())
}
