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
	"errors"
	"io"
	"net/http"
	"time"

	tbytes "github.com/trickstercache/trickster/v2/pkg/bytes"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/redact"
	tpe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// fetchDecoder decodes a 200's body, which resp.Body reads as it arrives
type fetchDecoder func(resp *http.Response) (timeseries.Timeseries, error)

// fetched is an upstream response that fetchDecoded made: its 200's body decoded, nil when the body was
// empty, and the error decoding it; any other status's body is buffered in resp.Body
type fetched struct {
	resp      *http.Response
	ts        timeseries.Timeseries
	decodeErr error
}

// fetchDecoded fetches from the origin, decoding a 200's body as it's read. A non-nil error, a failed or
// oversized read, leaves nothing decoded; callers check it and resp.StatusCode both.
func (pr *proxyRequest) fetchDecoded(decode fetchDecoder) (*fetched, error) {
	o := pr.rsc.BackendOptions
	pc := pr.rsc.PathConfig

	var handlerName string
	if pc != nil {
		handlerName = pc.HandlerName
	}

	start := time.Now()
	reader, resp, contentLength := PrepareFetchReader(pr.upstreamRequest)
	f := &fetched{resp: resp}

	var limit int64
	if o != nil && o.MaxObjectSizeBytes > 0 {
		// +1 so reaching limit means overflow, not exactly-at-limit.
		limit = int64(o.MaxObjectSizeBytes) + 1
	}
	var n int64
	var err error
	end := start
	switch {
	case reader == nil:
	case resp.StatusCode != http.StatusOK:
		// an error's body is read whole, for its detail
		var body []byte
		if limit > 0 {
			body, err = tbytes.ReadAllSized(io.LimitReader(reader, limit), min(contentLength, limit))
			if err == nil && int64(len(body)) >= limit {
				err = pr.tooLarge()
			}
		} else {
			body, err = io.ReadAll(reader)
		}
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		n, end = int64(len(body)), time.Now()
	default:
		body := &fetchBody{r: reader, limit: limit}
		upstream := resp.Body
		resp.Body = body
		ts, decodeErr := decode(resp)
		// the body is read to its end whatever the decoder took, so its size, limit and read errors
		// are as they were when it was read whole before decoding
		_, _ = io.Copy(io.Discard, body)
		upstream.Close()
		resp.Body = http.NoBody
		n, end = body.n, body.end
		switch {
		case body.over:
			err = pr.tooLarge()
		case body.err != nil:
			err = body.err
		case n > 0:
			f.ts, f.decodeErr = ts, decodeErr
		}
	}
	if err != nil {
		logger.Error("error reading body from http response",
			logging.Pairs{keys.URL: redact.URL(pr.URL), keys.Detail: err.Error()})
		return f, err
	}

	elapsed := end.Sub(start) // the time until the whole body was read, which a decode overlaps
	if resp != nil {
		pr.rsc.SetUpstream(pr.upstreamRequest.URL.Host, resp.StatusCode, elapsed)
	}

	// the client request is shared with the other fetches and the caller, who may change its headers
	// once this returns, so what the log needs of it is read now
	userAgent := pr.UserAgent()
	goWithRecover("proxyRequest.fetchDecoded.logUpstreamRequest", func() {
		logUpstreamRequest(o.Name, o.Provider, handlerName, pr.upstreamRequest.Method,
			redact.URL(pr.upstreamRequest.URL), userAgent, resp.StatusCode, int(n), elapsed.Seconds())
	})
	return f, nil
}

func (pr *proxyRequest) tooLarge() error {
	logger.Error("upstream response exceeded MaxObjectSizeBytes",
		logging.Pairs{keys.URL: redact.URL(pr.URL), "max": pr.rsc.BackendOptions.MaxObjectSizeBytes})
	return tpe.ErrUnexpectedUpstreamResponse
}

// fetchBody reads an upstream body for a decoder: failing past its limit (0 for none), and counting
// the bytes read, the first read error, and when the body ended
type fetchBody struct {
	r     io.Reader
	limit int64
	n     int64
	over  bool
	err   error
	end   time.Time
}

func (b *fetchBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.limit > 0 && b.n >= b.limit {
		b.over = true
		return n, tpe.ErrUnexpectedUpstreamResponse
	}
	switch {
	case errors.Is(err, io.EOF):
		if b.end.IsZero() {
			b.end = time.Now()
		}
	case err != nil && b.err == nil:
		b.err = err
	}
	return n, err
}

// Close leaves the upstream body to the fetch, which closes it once the body is read
func (b *fetchBody) Close() error {
	return nil
}
