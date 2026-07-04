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
	"fmt"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing"
	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	otlpgrpc "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otlphttp "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
)

var errGRPCPathOnlyEndpoint = fmt.Errorf("%w: path-only endpoints require OTLP/HTTP protocol",
	errs.ErrInvalidEndpointURL)

// New returns a new OTLP Tracer based on the provided options
func New(o *options.Options) (*tracing.Tracer, error) {
	if o == nil {
		return nil, errs.ErrNoTracerOptions
	}

	tags := make([]attribute.KeyValue, 1, len(o.Tags)+1)
	tags[0] = attribute.String("service.name", o.ServiceName)
	for k, v := range o.Tags {
		tags = append(tags, attribute.String(k, v))
	}

	exporter, err := newExporter(context.Background(), o)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(tracing.Sampler(o)),
		sdktrace.WithResource(resource.NewWithAttributes("", tags...)),
		sdktrace.WithBatcher(exporter),
	)
	tracer := tp.Tracer(o.Name)

	return &tracing.Tracer{
		Name:         o.Name,
		Tracer:       tracer,
		Options:      o,
		ShutdownFunc: exporter.Shutdown,
	}, nil
}

func newExporter(ctx context.Context, o *options.Options) (*otlptrace.Exporter, error) {
	switch o.Protocol {
	case "", options.OTLPProtocolHTTP:
		return newHTTPExporter(ctx, o)
	case options.OTLPProtocolGRPC:
		return newGRPCExporter(ctx, o)
	}
	return nil, fmt.Errorf("invalid OTLP protocol [%s]", o.Protocol)
}

func newHTTPExporter(ctx context.Context, o *options.Options) (*otlptrace.Exporter, error) {
	opts := make([]otlphttp.Option, 0, 10)
	// this determines if the collector endpoint is a path, uri or url
	// and calls the appropriate Options decorator
	switch {
	case o.Endpoint == "":
	case strings.HasPrefix(o.Endpoint, "/"):
		opts = append(opts, otlphttp.WithURLPath(o.Endpoint))
	case strings.HasPrefix(o.Endpoint, "http"):
		opts = append(opts, otlphttp.WithEndpointURL(o.Endpoint))
		if !strings.HasPrefix(o.Endpoint, "https") {
			opts = append(opts, otlphttp.WithInsecure())
		}
	default:
		opts = append(opts, otlphttp.WithEndpoint(o.Endpoint))
	}

	if o.Timeout > 0 {
		opts = append(opts, otlphttp.WithTimeout(o.Timeout))
	}

	if len(o.Headers) > 0 {
		opts = append(opts, otlphttp.WithHeaders(o.Headers))
	}

	if !o.DisableCompression {
		opts = append(opts, otlphttp.WithCompression(otlphttp.GzipCompression))
	}

	return otlphttp.New(ctx, opts...)
}

func newGRPCExporter(ctx context.Context, o *options.Options) (*otlptrace.Exporter, error) {
	opts := make([]otlpgrpc.Option, 0, 10)
	switch {
	case o.Endpoint == "":
	case strings.HasPrefix(o.Endpoint, "/"):
		return nil, errGRPCPathOnlyEndpoint
	case strings.HasPrefix(o.Endpoint, "http"):
		opts = append(opts, otlpgrpc.WithEndpointURL(o.Endpoint))
	default:
		opts = append(opts, otlpgrpc.WithEndpoint(o.Endpoint))
	}

	if o.Timeout > 0 {
		opts = append(opts, otlpgrpc.WithTimeout(o.Timeout))
	}

	if len(o.Headers) > 0 {
		opts = append(opts, otlpgrpc.WithHeaders(o.Headers))
	}

	if !o.DisableCompression {
		opts = append(opts, otlpgrpc.WithCompressor(grpcgzip.Name))
	}

	return otlpgrpc.New(ctx, opts...)
}
