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

// Package http provides the outbound-HTTP poller.Source: it owns the
// client, the per-iteration request, and the response lifecycle, and hands
// each response to a Handler that interprets it.
//
// Everything protocol-specific stays outside this package. Authentication
// and per-iteration request shaping go through a RequestDecorator -- SigV4
// signing, OAuth2 bearer tokens, a Consul ACL token, or the index/wait
// parameters of a blocking query -- so that no cloud or registry specifics
// leak in here. Interpretation of the response, including what counts as an
// acceptable status code, belongs to the Handler.
//
// Deadlines have exactly one owner: the poller's iteration context. This
// package deliberately does not set http.Client.Timeout, because a second
// whole-request deadline underneath the first is how a blocking query with
// a five-minute server-side wait gets truncated at thirty seconds. Options'
// Timeout configures the connect-phase sub-timeouts only.
package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery/poller"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
)

var (
	// ErrNilHandler is returned by NewSource when no Handler is provided
	ErrNilHandler = errors.New("http poll source requires a non-nil handler")
	// ErrNoURL is returned by NewSource when no URL is provided
	ErrNoURL = errors.New("http poll source requires a url")
	// ErrClientConflict is returned by NewSource when a caller-supplied
	// Client is combined with options that would configure a client this
	// package is no longer building, rather than silently ignoring them
	ErrClientConflict = errors.New(
		"http poll source cannot apply tls or transport options to a caller-supplied client")
)

// ErrUnexpectedStatus reports a response status a Handler did not accept.
// It wraps nothing; use errors.Is to detect it and the message for detail.
var ErrUnexpectedStatus = errors.New("unexpected http response status")

const (
	// DefaultTimeout is the connect-phase timeout applied when Options
	// leaves Timeout unset
	DefaultTimeout = 10 * time.Second
	// DefaultKeepAlive is the idle keep-alive applied when Options leaves
	// TransportOptions.KeepAlive unset
	DefaultKeepAlive = 30 * time.Second
	// drainLimit bounds how much of an unread response body is drained to
	// make the connection reusable. Beyond this, closing without draining
	// is cheaper than reading.
	drainLimit = 64 * 1024
)

// RequestDecorator mutates a request immediately before it is sent. It runs
// on every iteration, so it is the right place to refresh a credential, sign
// a request, or advance a blocking query's index parameter. Returning an
// error fails that iteration without sending anything.
type RequestDecorator func(ctx context.Context, r *http.Request) error

// Handler consumes one response and reports the wait before the next
// iteration, following poller.Source's convention: zero means "use the
// poller's interval", poller.PollNow means "immediately", and a positive
// value overrides the interval once.
//
// The Handler owns interpretation, including status codes -- health checks
// assert on specific codes while discovery providers want 2xx, and this
// package takes no position. It must not retain the response or its body,
// both of which are closed once it returns.
type Handler func(ctx context.Context, resp *http.Response) (time.Duration, error)

// TransportOptions tunes the connection pool. The defaults are sized for
// the common case of one poller against one endpoint; providers that
// paginate a cloud API across many round trips should raise them.
type TransportOptions struct {
	// KeepAlive is the idle connection keep-alive; zero selects
	// DefaultKeepAlive
	KeepAlive time.Duration
	// MaxIdleConns bounds idle connections; zero selects 1
	MaxIdleConns int
	// MaxIdleConnsPerHost bounds idle connections per host; zero selects 1
	MaxIdleConnsPerHost int
	// MaxConnsPerHost bounds total connections per host; zero selects 1
	MaxConnsPerHost int
}

// Options configures an HTTP poll source.
type Options struct {
	// URL is the endpoint to poll. Required unless Client is supplied with
	// a request the Decorator fully rewrites.
	URL string
	// Method defaults to GET
	Method string
	// Headers are set on every request, before Decorate runs
	Headers map[string]string
	// Body is sent on every request; it is held as bytes rather than a
	// reader because each iteration needs its own
	Body []byte
	// Timeout bounds the connect phase: dial, TLS handshake, and the wait
	// for response headers. It is deliberately not a whole-request
	// deadline -- that belongs to the poller's iteration context, so that
	// a long blocking query is not cut short from underneath. Zero selects
	// DefaultTimeout.
	Timeout time.Duration
	// TLS configures outbound client TLS via the shared builder
	TLS *to.Options
	// FollowRedirects allows the client to follow redirects. It defaults
	// false: a poller wants the response from the endpoint it was pointed
	// at, and a redirect to somewhere else is a fact the Handler should
	// see rather than something to chase silently.
	FollowRedirects bool
	// Transport tunes the connection pool
	Transport TransportOptions
	// Client, when set, is used as-is; TLS and Transport must then be
	// unset, since they would have no effect. It exists for callers that
	// already own a configured client, such as a backend handing its own
	// client to its health check.
	Client *http.Client
	// Decorate runs on every request immediately before it is sent
	Decorate RequestDecorator
}

// source is the poller.Source returned by NewSource.
type source struct {
	client   *http.Client
	method   string
	url      string
	headers  map[string]string
	body     []byte
	decorate RequestDecorator
	handler  Handler
}

// NewSource returns a poller.Source that issues one HTTP request per
// iteration and hands the response to h.
func NewSource(o *Options, h Handler) (poller.Source, error) {
	if o == nil {
		return nil, ErrNoURL
	}
	if h == nil {
		return nil, ErrNilHandler
	}
	if o.URL == "" {
		return nil, ErrNoURL
	}
	client, err := clientFor(o)
	if err != nil {
		return nil, err
	}
	method := o.Method
	if method == "" {
		method = http.MethodGet
	}
	// validate the URL and method now, at construction, rather than once
	// per iteration forever after
	if _, err := http.NewRequest(method, o.URL, nil); err != nil {
		return nil, err
	}
	return &source{
		client:   client,
		method:   method,
		url:      o.URL,
		headers:  o.Headers,
		body:     o.Body,
		decorate: o.Decorate,
		handler:  h,
	}, nil
}

// NewClient builds the *http.Client this package would use for the given
// options, without the single-request Source around it.
//
// It exists for providers whose one poll is several requests -- a paginated
// cloud API, say -- which cannot use a Source that issues exactly one
// request per iteration, but should still get the same TLS, redirect and
// connection-pool behavior as every other HTTP-based provider.
func NewClient(o *Options) (*http.Client, error) {
	if o == nil {
		return nil, ErrNoURL
	}
	return clientFor(o)
}

// clientFor returns the caller's client, or builds one from the options.
func clientFor(o *Options) (*http.Client, error) {
	if o.Client != nil {
		if o.TLS != nil || o.Transport != (TransportOptions{}) {
			return nil, ErrClientConflict
		}
		return o.Client, nil
	}
	tlsConfig, err := o.TLS.ToClientTLSConfig()
	if err != nil {
		return nil, err
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	t := o.Transport
	if t.KeepAlive <= 0 {
		t.KeepAlive = DefaultKeepAlive
	}
	if t.MaxIdleConns <= 0 {
		t.MaxIdleConns = 1
	}
	if t.MaxIdleConnsPerHost <= 0 {
		t.MaxIdleConnsPerHost = 1
	}
	if t.MaxConnsPerHost <= 0 {
		t.MaxConnsPerHost = 1
	}
	c := &http.Client{
		// no Timeout: the poller's iteration context owns the overall
		// deadline; see the package comment
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: t.KeepAlive,
			}).DialContext,
			MaxIdleConns:          t.MaxIdleConns,
			MaxIdleConnsPerHost:   t.MaxIdleConnsPerHost,
			MaxConnsPerHost:       t.MaxConnsPerHost,
			IdleConnTimeout:       t.KeepAlive,
			TLSHandshakeTimeout:   timeout,
			ExpectContinueTimeout: timeout,
			ResponseHeaderTimeout: timeout,
			TLSClientConfig:       tlsConfig,
			// explicit: Go suppresses h2 auto-enable when DialContext or
			// TLSClientConfig is custom
			ForceAttemptHTTP2: true,
		},
	}
	if !o.FollowRedirects {
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return c, nil
}

// Poll implements poller.Source.
func (s *source) Poll(ctx context.Context) (time.Duration, error) {
	var body io.Reader
	if len(s.body) > 0 {
		body = bytes.NewReader(s.body)
	}
	r, err := http.NewRequestWithContext(ctx, s.method, s.url, body)
	if err != nil {
		return 0, err
	}
	for k, v := range s.headers {
		r.Header.Set(k, v)
	}
	if s.decorate != nil {
		if err := s.decorate(ctx, r); err != nil {
			return 0, err
		}
	}
	resp, err := s.client.Do(r)
	if err != nil {
		return 0, err
	}
	defer closeBody(resp)
	return s.handler(ctx, resp)
}

// closeBody drains a bounded prefix of any unread body before closing, so
// the connection returns to the pool instead of being torn down. Bodies
// larger than drainLimit are abandoned: re-establishing the connection is
// cheaper than reading megabytes nobody wants.
func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
	resp.Body.Close()
}

// CheckStatus reports an error unless the response carries one of the
// accepted status codes, defaulting to 200 when none are named. It is a
// convenience for Handlers that only want the happy path; Handlers with
// richer expectations should inspect the response themselves.
func CheckStatus(resp *http.Response, accepted ...int) error {
	if len(accepted) == 0 {
		accepted = []int{http.StatusOK}
	}
	if slices.Contains(accepted, resp.StatusCode) {
		return nil
	}
	return fmt.Errorf("%w: got %d, expected one of %v",
		ErrUnexpectedStatus, resp.StatusCode, accepted)
}
