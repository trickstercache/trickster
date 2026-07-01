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

package engines

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	otracing "github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestCacheSpansIncludeResourceAttributesAndStatus(t *testing.T) {
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("could not load configuration: %s", err)
	}
	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Fatal("could not load default cache")
	}

	tr, sr := tu.NewRecordingTracer(t)
	pc := &po.Options{Path: "/api/v1/query", HandlerName: "proxycache"}
	rsc := request.NewResources(conf.Backends["default"], pc, cache.Configuration(), cache, nil, tr)
	ctx := tc.WithResources(context.Background(), rsc)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     http.StatusText(http.StatusOK),
		Header:     make(http.Header),
	}
	resp.Header.Set(headers.NameContentType, headers.ValueTextPlain)
	doc := DocumentFromHTTPResponse(resp, []byte("ok"), nil)
	doc.ContentType = headers.ValueTextPlain

	if err = WriteCache(ctx, cache, "trace-key", doc, time.Minute, sets.New([]string{headers.ValueTextPlain}), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = QueryCache(ctx, cache, "trace-key", nil, nil); err != nil {
		t.Fatal(err)
	}

	tu.RequireSpanAttributes(t, sr, "WriteCache", resourceAttributeStrings(rsc))

	wantQuery := resourceAttributeStrings(rsc)
	wantQuery["cache.status"] = "hit"
	tu.RequireSpanAttributes(t, sr, "QueryCache", wantQuery)
}

func TestDoProxySpanIncludesResourceStatusAttributes(t *testing.T) {
	r, rsc, sr := newTracingFetchRequest(t, &mockRoundTripper{
		resp: &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("accepted"))),
		},
	})

	w := httptest.NewRecorder()
	DoProxy(w, r, true)
	if got := w.Result().StatusCode; got != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d", http.StatusAccepted, got)
	}

	want := resourceAttributeStrings(rsc)
	want["cache.status"] = "proxy-only"
	want["http.status_code"] = "202"
	tu.RequireSpanAttributes(t, sr, "ProxyRequest", want)
}

func TestPrepareFetchReaderSpansIncludeResourceStatusAttributes(t *testing.T) {
	r, rsc, sr := newTracingFetchRequest(t, &mockRoundTripper{
		resp: &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("created"))),
		},
	})

	reader, resp, _ := PrepareFetchReader(r)
	if resp == nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status %d, got %#v", http.StatusCreated, resp)
	}
	if reader != nil {
		_, _ = io.ReadAll(reader)
		reader.Close()
	}

	want := resourceAttributeStrings(rsc)
	want["http.status_code"] = "201"
	tu.RequireSpanAttributes(t, sr, "PrepareFetchReader", want)
	tu.RequireSpanAttributes(t, sr, "ProxyRequest", want)
}

func TestPrepareFetchReaderErrorSpansIncludeBadGatewayStatus(t *testing.T) {
	r, rsc, sr := newTracingFetchRequest(t, &mockRoundTripper{err: errors.New("dial failed")})

	reader, resp, _ := PrepareFetchReader(r)
	if reader != nil {
		t.Fatal("expected nil reader")
	}
	if resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected status %d, got %#v", http.StatusBadGateway, resp)
	}

	want := resourceAttributeStrings(rsc)
	want["http.status_code"] = "502"
	tu.RequireSpanAttributes(t, sr, "PrepareFetchReader", want)
	tu.RequireSpanAttributes(t, sr, "ProxyRequest", want)
}

func TestPrepareFetchReaderPropagatesIncomingTraceContextToOrigin(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	otracing.ConfigurePropagators()

	rt := &mockRoundTripper{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader([]byte("ok"))),
		},
	}
	r, rsc, sr := newTracingFetchRequest(t, rt)

	incomingTraceID := trace.TraceID{1, 3, 5, 7, 9, 11, 13, 15, 2, 4, 6, 8, 10, 12, 14, 16}
	incomingSpanID := trace.SpanID{8, 7, 6, 5, 4, 3, 2, 1}
	r.Header.Set("traceparent", "00-"+incomingTraceID.String()+"-"+incomingSpanID.String()+"-01")
	r.Header.Set("baggage", "tenant=alpha")

	r, requestSpan := tspan.PrepareRequest(r, rsc.Tracer)
	if requestSpan == nil {
		t.Fatal("expected request span")
	}
	defer requestSpan.End()
	r = request.SetResources(r, rsc)

	reader, resp, _ := PrepareFetchReader(r)
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %#v", http.StatusOK, resp)
	}
	if reader != nil {
		_, _ = io.ReadAll(reader)
		reader.Close()
	}

	if len(rt.reqs) != 1 {
		t.Fatalf("expected 1 upstream request, got %d", len(rt.reqs))
	}
	upstream := rt.reqs[0]
	if got := upstream.Header.Get("traceparent"); got == "" {
		t.Fatal("expected upstream traceparent header")
	}
	if got := upstream.Header.Get("baggage"); got != "tenant=alpha" {
		t.Fatalf("expected upstream baggage %q, got %q", "tenant=alpha", got)
	}

	extracted := otracing.DefaultPropagators().Extract(
		context.Background(),
		propagation.HeaderCarrier(upstream.Header),
	)
	upstreamSpanCtx := trace.SpanContextFromContext(extracted)
	if !upstreamSpanCtx.IsRemote() {
		t.Fatal("expected propagated span context to extract as remote")
	}
	if upstreamSpanCtx.TraceID() != incomingTraceID {
		t.Fatalf("upstream trace id = %s, want %s", upstreamSpanCtx.TraceID(), incomingTraceID)
	}
	if upstreamSpanCtx.SpanID() == incomingSpanID {
		t.Fatal("upstream traceparent must use Trickster's outbound span, not the incoming parent span")
	}
	if got := baggage.FromContext(extracted).Member("tenant").Value(); got != "alpha" {
		t.Fatalf("extracted baggage tenant: expected %q, got %q", "alpha", got)
	}

	proxySpan := latestEndedSpan(t, sr, "ProxyRequest")
	if upstreamSpanCtx.SpanID() != proxySpan.SpanContext().SpanID() {
		t.Fatalf("upstream parent span id = %s, want outbound ProxyRequest span id %s",
			upstreamSpanCtx.SpanID(), proxySpan.SpanContext().SpanID())
	}
	if proxySpan.SpanContext().TraceID() != incomingTraceID {
		t.Fatalf("ProxyRequest trace id = %s, want %s",
			proxySpan.SpanContext().TraceID(), incomingTraceID)
	}

	if proxySpan.Parent().TraceID() != incomingTraceID {
		t.Fatalf("ProxyRequest parent trace id = %s, want %s",
			proxySpan.Parent().TraceID(), incomingTraceID)
	}
}

func TestObjectProxyCacheRequestSpanIncludesResourceStatusAttributes(t *testing.T) {
	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusPartialContent, hdrs)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestHarness(ts, r)

	tr, sr := tu.NewRecordingTracer(t)
	rsc.Tracer = tr
	rsc.BackendOptions.MaxTTL = 15 * time.Second
	r.Header.Add(headers.NameRange, "bytes=0-3")
	r = request.SetResources(r, rsc)

	_, errs := testFetchOPC(r, http.StatusPartialContent, "test", map[string]string{"status": "kmiss"})
	for _, err := range errs {
		t.Error(err)
	}

	want := resourceAttributeStrings(rsc)
	want["cache.status"] = "kmiss"
	want["http.status_code"] = "206"
	tu.RequireSpanAttributes(t, sr, "ObjectProxyCacheRequest", want)
}

func newTracingFetchRequest(t *testing.T, rt http.RoundTripper) (*http.Request, *request.Resources, *tracetest.SpanRecorder) {
	t.Helper()

	conf, err := config.Load([]string{"-origin-url", "http://example.com/", "-provider", "test"})
	if err != nil {
		t.Fatalf("could not load configuration: %s", err)
	}
	o := conf.Backends["default"]
	o.HTTPClient = &http.Client{Transport: rt}

	tr, sr := tu.NewRecordingTracer(t)
	pc := &po.Options{
		Path:            "/objects",
		HandlerName:     "proxycache",
		RequestHeaders:  map[string]string{},
		ResponseHeaders: map[string]string{},
	}
	rsc := request.NewResources(o, pc, conf.Caches["default"], nil, nil, tr)

	r := httptest.NewRequest(http.MethodGet, "http://example.com/objects", nil)
	r = r.WithContext(tc.WithResources(r.Context(), rsc))
	return r, rsc, sr
}

func resourceAttributeStrings(rsc *request.Resources) map[string]string {
	attrs := rsc.TracingAttributes()
	out := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		out[string(kv.Key)] = kv.Value.AsString()
	}
	return out
}

func latestEndedSpan(t testing.TB, sr *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	spans := sr.Ended()
	for i := len(spans) - 1; i >= 0; i-- {
		if spans[i].Name() == name {
			return spans[i]
		}
	}
	t.Fatalf("span %q not found in %d ended spans", name, len(spans))
	return nil
}
