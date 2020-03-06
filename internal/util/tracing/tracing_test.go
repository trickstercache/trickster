/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package tracing

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/api/trace"
	"go.opentelemetry.io/otel/exporter/trace/stdout"
	"go.opentelemetry.io/otel/plugin/httptrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/codes"
)

func TestMain(m *testing.M) {
	// Create stdout exporter to be able to retrieve
	// the collected spans.
	exporter, err := stdout.NewExporter(stdout.Options{PrettyPrint: true})
	if err != nil {
		fmt.Println("Test Setup Failure", err)
		os.Exit(-1)
	}

	// For the demonstration, use sdktrace.AlwaysSample sampler to sample all traces.
	// In a production application, use sdktrace.ProbabilitySampler with a desired probability.
	_, err = sdktrace.NewProvider(sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.AlwaysSample()}),
		sdktrace.WithSyncer(exporter))
	if err != nil {
		fmt.Println("Test Setup Failure", err)
		os.Exit(-1)
	}
	os.Exit(m.Run())
}

type panicTest struct {
	ctx          context.Context
	tracer       TracerImplementation
	exporter     TraceExporter
	rate         float64
	collectorURL string
}

func TestNoPanics(t *testing.T) {
	tests := []panicTest{
		{
			nil,
			OpenTelemetryTracer,
			JaegerExporter,
			1.0,
			"",
		},
		{
			makeCTX(
				func(ctx context.Context) context.Context {
					ctx = context.WithValue(ctx, TracerImplementation(345), 2345)
					return ctx
				},
			),
			100,
			7883,
			1000000000.0,
			"not a valid url",
		},
		{
			makeCTX(
				func(ctx context.Context) context.Context {
					var c context.CancelFunc
					ctx, c = context.WithTimeout(ctx, time.Duration(1*time.Millisecond))
					_ = c
					return ctx
				},
			),
			OpenTelemetryTracer,
			StdoutExporter,
			-1.0,
			"",
		},
		{
			context.Background(),
			-4,
			-1,
			1.0,
			"tcp://127.0.0.1",
		},
		{
			nil,
			OpenTracingTracer,
			-99,
			-.1,
			"",
		},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Panic %+v", r)
		}
	}()
	noPanic(t, tests)
}

func noPanic(t *testing.T, tests []panicTest) {

	for _, test := range tests {
		tr, flush, _, _ := SetTracer(test.tracer, test.exporter, test.collectorURL, test.rate)
		if tr != nil {
			ctx, span := NewChildSpan(test.ctx, tr, "TestNoPanics")
			spanCall(ctx, tr)
			if span != nil {
				span.End()
			}
			flush()
		}
	}

}

func TestRecorderTrace(t *testing.T) {
	flush, ctx, _, tr := setupTestingTracer(t, RecorderExporter, 1.0, testContextValues)
	defer flush()

	var res *http.Response

	err := tr.WithSpan(ctx, "Testing trace with span",
		func(ctx context.Context) error {
			var err error
			req, _ := http.NewRequest("GET", "https://example.com/test", nil)

			ctx, req = httptrace.W3C(ctx, req)
			httptrace.Inject(ctx, req)
			res, err = testHTTPClient().Do(req)
			if err != nil {
				return err
			}

			SpanFromContext(ctx).SetStatus(codes.OK)

			return nil

		})

	ctxValues := res.Header.Get("Correlation-Context")
	pairs := strings.Split(ctxValues, ",")
	m := make(map[string]string)
	for _, kv := range testContextValues {
		m[string(kv.Key)] = kv.Value.Emit()

	}

	for _, kv := range pairs {
		kvs := strings.Split(kv, "=")
		wantV := m[kvs[0]]
		v := kvs[1]
		assert.Equal(t, wantV, v, "distributed context value mismatch")

	}

	//TODO inspect captured span export via recorder

	assert.NoError(t, err, "Error adding span to test trace")
	_ = res.Body.Close()

}
func spanCall(ctx context.Context, tr trace.Tracer) {
	tr.WithSpan(ctx, "Testing trace with span",
		func(ctx context.Context) error {
			var err error
			req, _ := http.NewRequest("GET", "https://example.com/test", nil)
			ctx, req = httptrace.W3C(ctx, req)
			httptrace.Inject(ctx, req)
			_, err = testHTTPClient().Do(req)
			if err != nil {
				return err
			}
			SpanFromContext(ctx).SetStatus(codes.OK)
			return nil

		})

}
