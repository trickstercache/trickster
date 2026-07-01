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
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
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
