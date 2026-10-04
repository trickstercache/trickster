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

package engines

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/stretchr/testify/require"
)

type stubTransport func(*http.Request) (*http.Response, error)

func (f stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// closeTrackedBody is an upstream body that records whether it was closed
type closeTrackedBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackedBody) Close() error {
	b.closed = true
	return nil
}

// fetchFrom fetches a response of status, header and body through a backend of the given limit
func fetchFrom(t *testing.T, limit, status int, header http.Header, body io.Reader,
	decode fetchDecoder,
) (*fetched, *closeTrackedBody, error) {
	f, tb, _, err := fetchWith(t, limit, status, header, body, decode)
	return f, tb, err
}

// fetchWith is fetchFrom, also returning the request's resources
func fetchWith(t *testing.T, limit, status int, header http.Header, body io.Reader,
	decode fetchDecoder,
) (*fetched, *closeTrackedBody, *request.Resources, error) {
	t.Helper()
	ts, _, r, rsc, err := setupTestHarnessDPC()
	require.NoError(t, err)
	t.Cleanup(func() { closeTestHarness(ts, r) })
	tb := &closeTrackedBody{Reader: body}
	rsc.BackendOptions.MaxObjectSizeBytes = limit
	rsc.BackendOptions.HTTPClient = &http.Client{Transport: stubTransport(func(req *http.Request) (*http.Response, error) {
		if body == nil {
			return nil, errors.New("unreachable")
		}
		if header == nil {
			header = http.Header{}
		}
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status), Header: header, Body: tb,
			ContentLength: -1, Request: req,
		}, nil
	})}
	f, err := newProxyRequest(r, nil).fetchDecoded(decode)
	return f, tb, rsc, err
}

// readDecoder reads up to n bytes of the body (all when n < 0) into got, failing an empty body
func readDecoder(got *[]byte, n int) fetchDecoder {
	return func(resp *http.Response) (timeseries.Timeseries, error) {
		tr, dec := getTimeseriesReader(resp)
		defer closeDecoder(dec)
		r := tr
		if n >= 0 {
			r = io.LimitReader(tr, int64(n))
		}
		b, err := io.ReadAll(r)
		*got = b
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return nil, timeseries.ErrInvalidBody
		}
		return &dataset.DataSet{}, nil
	}
}

func TestFetchDecoded(t *testing.T) {
	const body = "hello, upstream"
	t.Run("a 200 is decoded as it's read", func(t *testing.T) {
		var got []byte
		f, tb, err := fetchFrom(t, len(body), http.StatusOK, nil, strings.NewReader(body), readDecoder(&got, -1))
		require.NoError(t, err)
		require.NotNil(t, f.ts)
		require.NoError(t, f.decodeErr)
		require.Equal(t, body, string(got))
		require.True(t, tb.closed)
		require.Equal(t, http.NoBody, f.resp.Body)
	})
	t.Run("an empty 200 decodes nothing and fails nothing", func(t *testing.T) {
		var got []byte
		f, _, err := fetchFrom(t, 0, http.StatusOK, nil, strings.NewReader(""), readDecoder(&got, -1))
		require.NoError(t, err)
		require.Nil(t, f.ts)
		require.NoError(t, f.decodeErr)
	})
	t.Run("a decoder that stops early leaves the rest to be read", func(t *testing.T) {
		upstream := strings.NewReader(body)
		f, tb, err := fetchFrom(t, 0, http.StatusOK, nil, upstream, func(resp *http.Response) (timeseries.Timeseries, error) {
			_, _ = io.ReadFull(resp.Body, make([]byte, 2))
			return nil, timeseries.ErrInvalidBody
		})
		require.NoError(t, err)
		require.ErrorIs(t, f.decodeErr, timeseries.ErrInvalidBody)
		require.Zero(t, upstream.Len())
		require.True(t, tb.closed)
	})
	t.Run("a body past the limit fails, decoded or not", func(t *testing.T) {
		for _, n := range []int{-1, 1} {
			var got []byte
			f, _, err := fetchFrom(t, len(body)-1, http.StatusOK, nil, strings.NewReader(body), readDecoder(&got, n))
			require.ErrorIs(t, err, tpe.ErrUnexpectedUpstreamResponse)
			require.Nil(t, f.ts)
			require.NoError(t, f.decodeErr)
		}
	})
	t.Run("a read error fails the fetch", func(t *testing.T) {
		boom := errors.New("connection reset")
		var got []byte
		f, _, err := fetchFrom(t, 0, http.StatusOK, nil,
			io.MultiReader(strings.NewReader(body), iotest.ErrReader(boom)), readDecoder(&got, -1))
		require.ErrorIs(t, err, boom)
		require.Nil(t, f.ts)
		// after the decoder stops, too
		_, _, err = fetchFrom(t, 0, http.StatusOK, nil,
			io.MultiReader(strings.NewReader(body), iotest.ErrReader(boom)), readDecoder(&got, 2))
		require.ErrorIs(t, err, boom)
	})
	t.Run("an encoded body is decoded", func(t *testing.T) {
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write([]byte(body))
		require.NoError(t, zw.Close())
		var got []byte
		f, _, err := fetchFrom(t, gz.Len(), http.StatusOK, http.Header{headers.NameContentEncoding: {"gzip"}},
			&gz, readDecoder(&got, -1))
		require.NoError(t, err)
		require.NotNil(t, f.ts)
		require.Equal(t, body, string(got))
	})
	t.Run("an error's body is buffered, not decoded", func(t *testing.T) {
		decode := func(*http.Response) (timeseries.Timeseries, error) {
			t.Fatal("an error's body was decoded")
			return nil, nil
		}
		f, _, err := fetchFrom(t, 0, http.StatusBadRequest, nil, strings.NewReader(body), decode)
		require.NoError(t, err)
		b, _ := io.ReadAll(f.resp.Body)
		require.Equal(t, body, string(b))
		_, _, err = fetchFrom(t, len(body)-1, http.StatusBadRequest, nil, strings.NewReader(body), decode)
		require.ErrorIs(t, err, tpe.ErrUnexpectedUpstreamResponse)
	})
	t.Run("a read's first error is the fetch's", func(t *testing.T) {
		first, second := errors.New("first"), errors.New("second")
		errs := []error{first, second}
		var got []byte
		_, _, err := fetchFrom(t, 0, http.StatusOK, nil, readerFunc(func([]byte) (int, error) {
			err := errs[0]
			errs = errs[min(1, len(errs)-1):]
			return 0, err
		}), readDecoder(&got, -1))
		require.ErrorIs(t, err, first)
	})
	t.Run("the upstream's time ends when its body does", func(t *testing.T) {
		ts, _, r, rsc, err := setupTestHarnessDPC()
		require.NoError(t, err)
		t.Cleanup(func() { closeTestHarness(ts, r) })
		var transportStart, bodyEnd time.Time
		rsc.BackendOptions.HTTPClient = &http.Client{Transport: stubTransport(func(req *http.Request) (*http.Response, error) {
			transportStart = time.Now()
			return &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1, Request: req,
			}, nil
		})}
		pr := newProxyRequest(r, nil)
		before := time.Now()
		_, err = pr.fetchDecoded(func(resp *http.Response) (timeseries.Timeseries, error) {
			_, _ = io.ReadAll(resp.Body)
			bodyEnd = resp.Body.(*fetchBody).end
			require.False(t, bodyEnd.IsZero(), "EOF records the end before decoding finishes")
			time.Sleep(20 * time.Millisecond)
			return &dataset.DataSet{}, nil
		})
		require.NoError(t, err)
		_, _, elapsed := rsc.Upstream()
		require.GreaterOrEqual(t, elapsed, bodyEnd.Sub(transportStart))
		require.LessOrEqual(t, elapsed, bodyEnd.Sub(before))
	})
	t.Run("an unreachable origin is its status", func(t *testing.T) {
		f, _, err := fetchFrom(t, 0, http.StatusOK, nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusBadGateway, f.resp.StatusCode)
		require.Nil(t, f.ts)
	})
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) {
	return f(p)
}

// cutBody gives its body's first n bytes, then fails
type cutBody struct {
	io.ReadCloser
	n int
}

func (b *cutBody) Read(p []byte) (int, error) {
	if b.n <= 0 {
		return 0, errors.New("connection reset")
	}
	if len(p) > b.n {
		p = p[:b.n]
	}
	n, err := b.ReadCloser.Read(p)
	b.n -= n
	return n, err
}

func TestDeltaProxyCacheFailedReadIsNotCached(t *testing.T) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	require.NoError(t, err)
	defer closeTestHarness(ts, r)
	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions
	o.FastForwardDisable = true
	step := 300 * time.Second
	end := time.Now().Add(-12 * time.Hour)
	extr := timeseries.Extent{Start: end.Add(-18 * time.Hour), End: end}
	r.URL.Path = "/prometheus/api/v1/query_range"
	r.URL.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)
	upstream := o.HTTPClient.Transport
	if upstream == nil {
		upstream = http.DefaultTransport
	}
	fail := true
	o.HTTPClient.Transport = stubTransport(func(req *http.Request) (*http.Response, error) {
		resp, err := upstream.RoundTrip(req)
		if err == nil && fail {
			resp.Body = &cutBody{ReadCloser: resp.Body, n: 64}
		}
		return resp, err
	})
	serve := func() string {
		w := httptest.NewRecorder()
		client.QueryRangeHandler(w, r)
		return w.Header().Get(headers.NameTricksterResult)
	}
	// a body that fails partway caches nothing, so the next request fetches it whole
	serve()
	fail = false
	require.Contains(t, serve(), "kmiss")
	require.Contains(t, serve(), "status=hit")
}
