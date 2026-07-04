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

// Package otlp provides a OTLP Tracer
package otlp

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"go.opentelemetry.io/otel/trace"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

func TestNew(t *testing.T) {
	_, err := New(nil)
	if err != errs.ErrNoTracerOptions {
		t.Error("expected error for no tracer options")
	}

	opt := options.New()
	opt.Tags = map[string]string{"test": "test"}
	opt.Endpoint = "1.2.3.4:8000"

	_, err = New(opt)
	if err != nil {
		t.Error(err)
	}

	opt.SampleRate = new(0.0)
	_, err = New(opt)
	if err != nil {
		t.Error(err)
	}

	opt.SampleRate = new(0.5)
	_, err = New(opt)
	if err != nil {
		t.Error(err)
	}

	opt.Protocol = "udp"
	_, err = New(opt)
	if err == nil {
		t.Error("expected error for invalid protocol")
	}

	opt.Protocol = options.OTLPProtocolGRPC
	opt.Endpoint = "/v1/traces"
	_, err = New(opt)
	if err != errGRPCPathOnlyEndpoint {
		t.Errorf("expected invalid endpoint error, got %v", err)
	}
}

func TestNewAppliesEndpointOptions(t *testing.T) {
	tests := []struct {
		name     string
		endpoint func(*httptest.Server) string
		env      map[string]string
		wantPath string
	}{
		{
			name: "hostport endpoint",
			endpoint: func(s *httptest.Server) string {
				return strings.TrimPrefix(s.URL, "http://")
			},
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_TRACES_INSECURE": "true",
			},
			wantPath: "/v1/traces",
		},
		{
			name: "url endpoint",
			endpoint: func(s *httptest.Server) string {
				return s.URL + "/custom/traces"
			},
			wantPath: "/custom/traces",
		},
		{
			name: "path endpoint",
			endpoint: func(_ *httptest.Server) string {
				return "/path-only/traces"
			},
			env: map[string]string{
				"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://example.invalid/ignored",
			},
			wantPath: "/path-only/traces",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan *http.Request, 10)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				select {
				case requests <- r.Clone(context.Background()):
				default:
				}
				w.Header().Set("Content-Type", "application/x-protobuf")
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			for k, v := range tc.env {
				if k == "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT" {
					v = strings.Replace(v, "http://example.invalid", srv.URL, 1)
				}
				t.Setenv(k, v)
			}
			t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")

			opt := options.New()
			opt.Endpoint = tc.endpoint(srv)
			opt.Headers = map[string]string{"X-Trickster-Test": tc.name}
			opt.Timeout = time.Second

			tr, err := New(opt)
			if err != nil {
				t.Fatal(err)
			}

			_, span := tr.Start(context.Background(), "test-span")
			span.End()

			select {
			case req := <-requests:
				if req.URL.Path != tc.wantPath {
					t.Errorf("expected request path %q, got %q", tc.wantPath, req.URL.Path)
				}
				if got := req.Header.Get("X-Trickster-Test"); got != tc.name {
					t.Errorf("expected custom header %q, got %q", tc.name, got)
				}
			case <-time.After(time.Second):
				t.Fatal("expected OTLP request to configured endpoint")
			}

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err = tr.ShutdownFunc(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNewAppliesResourceAttributes(t *testing.T) {
	payloads := make(chan *collectortracepb.ExportTraceServiceRequest, 10)
	handlerErrs := make(chan error, 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			handlerErrs <- err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req collectortracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			handlerErrs <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloads <- &req
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")

	opt := options.New()
	opt.Endpoint = srv.URL + "/v1/traces"
	opt.ServiceName = "trickster-test"
	opt.Tags = map[string]string{
		"component":              "proxy",
		"deployment.environment": "test",
	}
	opt.DisableCompression = true
	opt.Timeout = time.Second

	tr, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}

	_, span := tr.Start(context.Background(), "test-span")
	span.End()

	req := waitForOTLPRequest(t, payloads, handlerErrs)
	attrs := resourceAttributeValues(req)
	if got := attrs["service.name"]; got != "trickster-test" {
		t.Errorf("expected service.name %q, got %q", "trickster-test", got)
	}
	if got := attrs["component"]; got != "proxy" {
		t.Errorf("expected component tag %q, got %q", "proxy", got)
	}
	if got := attrs["deployment.environment"]; got != "test" {
		t.Errorf("expected deployment.environment tag %q, got %q", "test", got)
	}
	if _, ok := attrs[""]; ok {
		t.Error("unexpected empty resource attribute")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = tr.ShutdownFunc(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNewAppliesGRPCProtocol(t *testing.T) {
	payloads := make(chan *collectortracepb.ExportTraceServiceRequest, 10)
	metadataCh := make(chan metadata.MD, 10)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	collectortracepb.RegisterTraceServiceServer(srv, &recordingTraceService{
		payloads: payloads,
		metadata: metadataCh,
	})
	go func() {
		_ = srv.Serve(lis)
	}()
	defer srv.Stop()

	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")

	opt := options.New()
	opt.Protocol = options.OTLPProtocolGRPC
	opt.Endpoint = "http://" + lis.Addr().String()
	opt.ServiceName = "trickster-grpc-test"
	opt.Headers = map[string]string{"x-trickster-test": options.OTLPProtocolGRPC}
	opt.Tags = map[string]string{"component": "proxy"}
	opt.DisableCompression = true
	opt.Timeout = time.Second

	tr, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}

	_, span := tr.Start(context.Background(), "grpc-span")
	span.End()

	req := waitForOTLPRequest(t, payloads, nil)
	if !hasSpanName(req, "grpc-span") {
		t.Fatal("expected grpc-span span in OTLP request")
	}
	attrs := resourceAttributeValues(req)
	if got := attrs["service.name"]; got != "trickster-grpc-test" {
		t.Errorf("expected service.name %q, got %q", "trickster-grpc-test", got)
	}
	if got := attrs["component"]; got != "proxy" {
		t.Errorf("expected component tag %q, got %q", "proxy", got)
	}

	select {
	case md := <-metadataCh:
		if got := md.Get("x-trickster-test"); len(got) != 1 || got[0] != options.OTLPProtocolGRPC {
			t.Errorf("expected gRPC metadata header %q, got %v", options.OTLPProtocolGRPC, got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected OTLP gRPC metadata")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = tr.ShutdownFunc(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNewContinuesSampledRemoteParent(t *testing.T) {
	payloads := make(chan *collectortracepb.ExportTraceServiceRequest, 10)
	handlerErrs := make(chan error, 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			handlerErrs <- err
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req collectortracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			handlerErrs <- err
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		payloads <- &req
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("OTEL_BSP_MAX_EXPORT_BATCH_SIZE", "1")

	sampleRate := 0.0
	opt := options.New()
	opt.Endpoint = srv.URL + "/v1/traces"
	opt.SampleRate = &sampleRate
	opt.DisableCompression = true
	opt.Timeout = time.Second

	tr, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     trace.SpanID{17, 18, 19, 20, 21, 22, 23, 24},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	_, span := tr.Start(
		trace.ContextWithRemoteSpanContext(context.Background(), parent),
		"sampled-remote-child",
	)
	span.End()

	req := waitForOTLPRequest(t, payloads, handlerErrs)
	if !hasSpanName(req, "sampled-remote-child") {
		t.Fatalf("expected sampled-remote-child span in OTLP request")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = tr.ShutdownFunc(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitForOTLPRequest(t *testing.T, payloads <-chan *collectortracepb.ExportTraceServiceRequest,
	handlerErrs <-chan error) *collectortracepb.ExportTraceServiceRequest {
	t.Helper()
	select {
	case req := <-payloads:
		return req
	case err := <-handlerErrs:
		t.Fatalf("failed to decode OTLP request: %v", err)
	case <-time.After(time.Second):
		t.Fatal("expected OTLP request")
	}
	return nil
}

type recordingTraceService struct {
	collectortracepb.UnimplementedTraceServiceServer
	payloads chan<- *collectortracepb.ExportTraceServiceRequest
	metadata chan<- metadata.MD
}

func (s *recordingTraceService) Export(ctx context.Context,
	req *collectortracepb.ExportTraceServiceRequest,
) (*collectortracepb.ExportTraceServiceResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		s.metadata <- md.Copy()
	}
	s.payloads <- req
	return &collectortracepb.ExportTraceServiceResponse{}, nil
}

func resourceAttributeValues(req *collectortracepb.ExportTraceServiceRequest) map[string]string {
	out := map[string]string{}
	for _, rs := range req.GetResourceSpans() {
		for _, attr := range rs.GetResource().GetAttributes() {
			out[attr.GetKey()] = attr.GetValue().GetStringValue()
		}
	}
	return out
}

func hasSpanName(req *collectortracepb.ExportTraceServiceRequest, name string) bool {
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				if span.GetName() == name {
					return true
				}
			}
		}
	}
	return false
}
