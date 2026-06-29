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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
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
