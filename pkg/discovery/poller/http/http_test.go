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

package http

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/discovery/poller"
	to "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"

	"github.com/stretchr/testify/require"
)

// echoHandler returns a Handler that records the response it saw.
func bodyRecorder(into *string) Handler {
	return func(_ context.Context, resp *http.Response) (time.Duration, error) {
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, err
		}
		*into = string(b)
		return 0, nil
	}
}

func mustSource(t *testing.T, o *Options, h Handler) poller.Source {
	t.Helper()
	s, err := NewSource(o, h)
	require.NoError(t, err)
	return s
}

func TestNewSourceValidation(t *testing.T) {
	noop := Handler(func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	tests := map[string]struct {
		o    *Options
		h    Handler
		want error
	}{
		"nil options": {nil, noop, ErrNoURL},
		"nil handler": {&Options{URL: "http://example.com"}, nil, ErrNilHandler},
		"no url":      {&Options{}, noop, ErrNoURL},
		"client with tls options": {
			&Options{URL: "http://example.com", Client: http.DefaultClient, TLS: &to.Options{}},
			noop, ErrClientConflict,
		},
		"client with transport options": {
			&Options{
				URL: "http://example.com", Client: http.DefaultClient,
				Transport: TransportOptions{MaxIdleConns: 5},
			},
			noop, ErrClientConflict,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			s, err := NewSource(test.o, test.h)
			require.ErrorIs(t, err, test.want)
			require.Nil(t, s)
		})
	}
}

// An unusable URL or method should fail at construction, not once per
// iteration for the life of the process.
func TestNewSourceRejectsBadRequestAtConstruction(t *testing.T) {
	noop := Handler(func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	_, err := NewSource(&Options{URL: "://not-a-url"}, noop)
	require.Error(t, err)
	_, err = NewSource(&Options{URL: "http://example.com", Method: "bad method"}, noop)
	require.Error(t, err)
}

func TestPollDeliversResponseToHandler(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("hello"))
	}))
	defer ts.Close()
	var got string
	s := mustSource(t, &Options{URL: ts.URL}, bodyRecorder(&got))
	next, err := s.Poll(t.Context())
	require.NoError(t, err)
	require.Zero(t, next)
	require.Equal(t, "hello", got)
}

// The Handler's next wait is the source's next wait: this is how a blocking
// query says "re-issue now" and a TTL-bearing source names its own cadence.
func TestPollPropagatesHandlerNextWait(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	for _, want := range []time.Duration{poller.PollNow, 0, 42 * time.Second} {
		s := mustSource(t, &Options{URL: ts.URL},
			func(context.Context, *http.Response) (time.Duration, error) { return want, nil })
		next, err := s.Poll(t.Context())
		require.NoError(t, err)
		require.Equal(t, want, next)
	}
}

func TestPollSendsMethodHeadersAndBody(t *testing.T) {
	var method, header, body string
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		method = r.Method
		header = r.Header.Get("X-Test")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer ts.Close()
	s := mustSource(t, &Options{
		URL:     ts.URL,
		Method:  http.MethodPost,
		Headers: map[string]string{"X-Test": "yes"},
		Body:    []byte("payload"),
	}, func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	_, err := s.Poll(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, method)
	require.Equal(t, "yes", header)
	require.Equal(t, "payload", body)
}

// The body is held as bytes precisely so each iteration gets its own reader;
// a shared io.Reader would send an empty body on every poll after the first.
func TestBodyIsResentOnEveryIteration(t *testing.T) {
	var bodies []string
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
	}))
	defer ts.Close()
	s := mustSource(t, &Options{URL: ts.URL, Method: http.MethodPost, Body: []byte("payload")},
		func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	for range 3 {
		_, err := s.Poll(t.Context())
		require.NoError(t, err)
	}
	require.Equal(t, []string{"payload", "payload", "payload"}, bodies)
}

func TestDecoratorRunsOnEveryIterationAndCanRewriteTheRequest(t *testing.T) {
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RawQuery+"|"+r.Header.Get("Authorization"))
	}))
	defer ts.Close()
	var n atomic.Int64
	// models a blocking query advancing its index and a credential being
	// refreshed per iteration
	s := mustSource(t, &Options{
		URL: ts.URL,
		Decorate: func(_ context.Context, r *http.Request) error {
			i := n.Add(1)
			q := r.URL.Query()
			q.Set("index", string(rune('0'+i)))
			r.URL.RawQuery = q.Encode()
			r.Header.Set("Authorization", "token-"+string(rune('0'+i)))
			return nil
		},
	}, func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	for range 2 {
		_, err := s.Poll(t.Context())
		require.NoError(t, err)
	}
	require.Equal(t, []string{"index=1|token-1", "index=2|token-2"}, seen)
}

// A decorator that cannot produce a credential must fail the iteration
// without sending an unsigned request.
func TestDecoratorErrorFailsIterationWithoutSending(t *testing.T) {
	var called atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called.Store(true)
	}))
	defer ts.Close()
	errNoCreds := errors.New("no credentials")
	s := mustSource(t, &Options{
		URL:      ts.URL,
		Decorate: func(context.Context, *http.Request) error { return errNoCreds },
	}, func(context.Context, *http.Response) (time.Duration, error) {
		t.Error("handler ran despite a decorator error")
		return 0, nil
	})
	_, err := s.Poll(t.Context())
	require.ErrorIs(t, err, errNoCreds)
	require.False(t, called.Load(), "a request was sent despite the decorator failing")
}

// Non-2xx is the Handler's business, not the source's: health checks
// legitimately expect codes discovery would reject.
func TestSourcePassesNon2xxToHandler(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer ts.Close()
	var code int
	s := mustSource(t, &Options{URL: ts.URL},
		func(_ context.Context, resp *http.Response) (time.Duration, error) {
			code = resp.StatusCode
			return 0, nil
		})
	_, err := s.Poll(t.Context())
	require.NoError(t, err, "the source should not judge status codes")
	require.Equal(t, http.StatusTeapot, code)
}

func TestHandlerErrorPropagates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	errBoom := errors.New("boom")
	s := mustSource(t, &Options{URL: ts.URL},
		func(context.Context, *http.Response) (time.Duration, error) { return 0, errBoom })
	_, err := s.Poll(t.Context())
	require.ErrorIs(t, err, errBoom)
}

// Redirects are a fact the Handler should see, not something to chase.
func TestRedirectsAreNotFollowedByDefault(t *testing.T) {
	var reachedTarget atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedTarget.Store(true)
	}))
	defer target.Close()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer ts.Close()

	var code int
	record := func(_ context.Context, resp *http.Response) (time.Duration, error) {
		code = resp.StatusCode
		return 0, nil
	}
	s := mustSource(t, &Options{URL: ts.URL}, record)
	_, err := s.Poll(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusFound, code)
	require.False(t, reachedTarget.Load())

	s = mustSource(t, &Options{URL: ts.URL, FollowRedirects: true}, record)
	_, err = s.Poll(t.Context())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, code)
	require.True(t, reachedTarget.Load())
}

// The body must be closed however the handler returns, or the connection
// leaks; an unread body must also be drained so it can be reused.
func TestBodyIsClosedEvenWhenHandlerIgnoresIt(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer ts.Close()
	errBoom := errors.New("boom")
	s := mustSource(t, &Options{URL: ts.URL},
		func(context.Context, *http.Response) (time.Duration, error) { return 0, errBoom })
	for range 5 {
		_, err := s.Poll(t.Context())
		require.ErrorIs(t, err, errBoom)
	}
}

// The iteration context owns the deadline; the source must not hold a
// second one that could truncate a long poll.
func TestIterationContextOwnsTheDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	s := mustSource(t, &Options{URL: ts.URL},
		func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	require.Zero(t, s.(*source).client.Timeout,
		"the client must not carry a whole-request timeout")

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Poll(ctx)
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second,
		"the iteration was not bounded by its context deadline")
}

// A caller-supplied client is used as-is; this is how a backend hands its
// own configured client to its health check.
func TestCallerSuppliedClientIsUsed(t *testing.T) {
	var viaCustom atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer ts.Close()
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		viaCustom.Store(true)
		return http.DefaultTransport.RoundTrip(r)
	})}
	s := mustSource(t, &Options{URL: ts.URL, Client: c},
		func(context.Context, *http.Response) (time.Duration, error) { return 0, nil })
	_, err := s.Poll(t.Context())
	require.NoError(t, err)
	require.True(t, viaCustom.Load())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportErrorIsReturned(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing is listening now
	s := mustSource(t, &Options{URL: url, Timeout: 100 * time.Millisecond},
		func(context.Context, *http.Response) (time.Duration, error) {
			t.Error("handler ran despite a transport error")
			return 0, nil
		})
	_, err := s.Poll(t.Context())
	require.Error(t, err)
}

func TestCheckStatus(t *testing.T) {
	require.NoError(t, CheckStatus(&http.Response{StatusCode: http.StatusOK}))
	require.NoError(t, CheckStatus(&http.Response{StatusCode: http.StatusAccepted},
		http.StatusOK, http.StatusAccepted))
	err := CheckStatus(&http.Response{StatusCode: http.StatusNotFound})
	require.ErrorIs(t, err, ErrUnexpectedStatus)
	require.Contains(t, err.Error(), "404")
}

// The source satisfies poller.Source and drives a real loop end to end.
func TestSourceDrivesAPoller(t *testing.T) {
	var hits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer ts.Close()
	done := make(chan struct{})
	var once atomic.Bool
	s := mustSource(t, &Options{URL: ts.URL},
		func(_ context.Context, resp *http.Response) (time.Duration, error) {
			if err := CheckStatus(resp); err != nil {
				return 0, err
			}
			if once.CompareAndSwap(false, true) {
				close(done)
			}
			return 0, nil
		})
	p, err := poller.New(poller.Options{
		Name: "test", Interval: time.Hour, Jitter: -1,
	}, s)
	require.NoError(t, err)
	p.Start(t.Context())
	defer p.Stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the poller never polled")
	}
	require.EqualValues(t, 1, hits.Load())
}
