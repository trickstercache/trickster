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
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"golang.org/x/net/http/httpguts"
)

// upstreamFunc performs one upstream attempt.
type upstreamFunc func(*http.Request) (*http.Response, error)

func doUpstream(do upstreamFunc, r *http.Request, rsc *request.Resources) (*http.Response, error) {
	// the path's timeout and retry policy bound the attempts; a path without one costs a nil check
	var pc *po.Options
	if rsc != nil {
		pc = rsc.PathConfig
	}
	// a tunnel outlives the exchange that opened it, so an upgrade is neither
	// bounded nor retried
	if !pc.HasUpstreamPolicy() || isUpgrade(r) {
		return do(r)
	}
	var cancels cancelList
	ctx := r.Context()
	if pc.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(pc.Timeout))
		cancels = append(cancels, cancel)
		r = r.WithContext(ctx)
	}
	attempts := 1
	retry := pc.Retry
	if retry != nil && retry.Attempts > 0 && retryableRequest(r) {
		attempts += retry.Attempts
		retry.Budget().Request()
	}
	var backendName string
	if rsc != nil && rsc.BackendOptions != nil {
		backendName = rsc.BackendOptions.Name
	}
	var resp *http.Response
	var err error
	for i := range attempts {
		var body io.ReadCloser
		if i > 0 && r.GetBody != nil {
			var berr error
			if body, berr = r.GetBody(); berr != nil {
				break
			}
		}
		req, attemptCancel := attemptRequest(ctx, r, body, time.Duration(pc.AttemptTimeout))
		resp, err = do(req)
		last := i == attempts-1
		if last || !shouldRetry(ctx, resp, err, retry) || !retry.Budget().Allow() {
			if err != nil {
				attemptCancel()
				cancels.run()
				return resp, err
			}
			if resp.StatusCode == http.StatusSwitchingProtocols {
				// the origin upgraded unasked: the body is now a tunnel the caller
				// owns, and the deadlines expire on their own
				return resp, nil
			}
			resp.Body = append(cancels, attemptCancel).body(resp.Body)
			return resp, nil
		}
		// the failed attempt is closed rather than drained: an origin that sent
		// retryable headers and stalled would otherwise hold the retry
		discardAttempt(resp)
		attemptCancel()
		metrics.ProxyUpstreamRetries.WithLabelValues(backendName, pc.Path).Inc()
		if retry.Backoff > 0 && !wait(ctx, time.Duration(retry.Backoff)) {
			break
		}
	}
	cancels.run()
	if err == nil {
		err = ctx.Err()
		if err == nil {
			err = context.DeadlineExceeded
		}
	}
	return nil, err
}

func attemptRequest(ctx context.Context, r *http.Request, body io.ReadCloser,
	timeout time.Duration,
) (*http.Request, context.CancelFunc) {
	// one attempt's request: a copy carrying the replayed body and, when bounded, its own deadline
	if body == nil && timeout <= 0 {
		return r, func() {}
	}
	cancel := context.CancelFunc(func() {})
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	req := r.WithContext(ctx)
	if body != nil {
		req.Body = body
	}
	return req, cancel
}

func retryableRequest(r *http.Request) bool {
	// a request can be repeated when its method is idempotent and any body can be replayed
	if !po.IsIdempotent(r.Method) {
		return false
	}
	return r.Body == nil || r.Body == http.NoBody || r.GetBody != nil
}

func shouldRetry(ctx context.Context, resp *http.Response, err error, retry *po.RetryOptions) bool {
	// a request the client abandoned, or whose deadline ended, is never retried
	if retry == nil || ctx.Err() != nil {
		return false
	}
	if err != nil {
		return !errors.Is(err, context.Canceled)
	}
	return resp != nil && retry.Retries(resp.StatusCode)
}

func discardAttempt(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	resp.Body.Close()
}

func isUpgrade(r *http.Request) bool {
	return r.Header.Get(headers.NameUpgrade) != "" &&
		httpguts.HeaderValuesContainsToken(r.Header[headers.NameConnection], "Upgrade")
}

func wait(ctx context.Context, d time.Duration) bool {
	// sleeps for d unless ctx ends first, reporting whether the wait completed
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// cancelList holds the deadlines an upstream exchange opened; they are released
// when the response body is closed, since the body outlives the call.
type cancelList []context.CancelFunc

func (c cancelList) run() {
	for _, cancel := range c {
		cancel()
	}
}

func (c cancelList) body(rc io.ReadCloser) io.ReadCloser {
	if rc == nil {
		rc = http.NoBody
	}
	return &cancelBody{ReadCloser: rc, cancels: c}
}

type cancelBody struct {
	io.ReadCloser
	cancels cancelList
}

func (b *cancelBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancels.run()
	return err
}

func beginTrailerResponse(w io.Writer) {
	// the header goes ahead of the body so the server cannot settle on a length and drop the trailers
	if rw, ok := w.(http.ResponseWriter); ok {
		_ = http.NewResponseController(rw).Flush()
	}
}

func forwardTrailers(w io.Writer, resp *http.Response) {
	// the origin's trailers are relayed after the body; unannounced ones use the trailer prefix
	rw, ok := w.(http.ResponseWriter)
	if !ok || resp == nil || len(resp.Trailer) == 0 {
		return
	}
	h := rw.Header()
	for k, vv := range resp.Trailer {
		h[http.TrailerPrefix+k] = vv
	}
}
