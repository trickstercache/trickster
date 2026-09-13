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

package middleware

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	tctx "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"

	"github.com/prometheus/client_golang/prometheus"
)

// mirror results for the metric
const (
	mirrorResultSent    = "sent"
	mirrorResultDropped = "dropped"
)

type mirror struct {
	target   http.Handler
	percent  int
	limit    int64
	inflight atomic.Int64
	sent     prometheus.Counter
	dropped  prometheus.Counter
}

// Mirror sends a copy of a share of requests to target's router and discards the responses;
// a copy is served on its own goroutine, bounded in number, and never mirrored again.
func Mirror(backendName string, o *po.MirrorOptions, target backends.Backend, next http.Handler) http.Handler {
	if o == nil || target == nil || target.Router() == nil || next == nil {
		return next
	}
	m := &mirror{
		target:  target.Router(),
		percent: o.ResolvedPercent(),
		limit:   int64(o.ResolvedMaxInFlight()),
		sent:    metrics.ProxyMirrorRequests.WithLabelValues(backendName, target.Name(), mirrorResultSent),
		dropped: metrics.ProxyMirrorRequests.WithLabelValues(backendName, target.Name(), mirrorResultDropped),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// sampling a share of traffic needs no unpredictability
		if !tctx.IsMirrored(r.Context()) && (m.percent >= 100 || rand.IntN(100) < m.percent) { //nolint:gosec // traffic sampling
			m.fire(r)
		}
		next.ServeHTTP(w, r)
	})
}

func (m *mirror) fire(r *http.Request) {
	if m.inflight.Add(1) > m.limit {
		m.inflight.Add(-1)
		m.dropped.Inc()
		return
	}
	out, err := mirrorRequest(r)
	if err != nil {
		m.inflight.Add(-1)
		m.dropped.Inc()
		return
	}
	go func() {
		defer m.inflight.Add(-1)
		defer func() {
			if p := recover(); p != nil {
				logger.Error("mirrored request handler panicked", logging.Pairs{keys.Detail: p})
			}
		}()
		m.target.ServeHTTP(discardResponseWriter{header: make(http.Header)}, out)
		m.sent.Inc()
	}()
}

func mirrorRequest(r *http.Request) (*http.Request, error) {
	// a bodied request is buffered on the original's resources so both can read it, and the
	// copy is detached from the client's context so neither cancels the other
	var body io.Reader = http.NoBody
	var length int64
	if methods.HasBody(r.Method) {
		b, err := request.GetBody(r)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
		length = int64(len(b))
	}
	ctx := tctx.WithMirrored(context.Background())
	out, err := http.NewRequestWithContext(ctx, r.Method, r.URL.String(), body)
	if err != nil {
		return nil, err
	}
	out.ContentLength = length
	out.Header = r.Header.Clone()
	out.Host = r.Host
	out.RemoteAddr = r.RemoteAddr
	out.RequestURI = r.RequestURI
	out.Proto, out.ProtoMajor, out.ProtoMinor = r.Proto, r.ProtoMajor, r.ProtoMinor
	return out, nil
}

// discardResponseWriter accepts a mirrored response and drops it.
type discardResponseWriter struct {
	header http.Header
}

func (d discardResponseWriter) Header() http.Header         { return d.header }
func (d discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d discardResponseWriter) WriteHeader(int)             {}
