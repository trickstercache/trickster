/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package tracing

import (
	"context"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/api/global"
	"go.opentelemetry.io/otel/exporter/trace/stdout"
	"go.opentelemetry.io/otel/plugin/httptrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/codes"
)

func init() {
	// Create stdout exporter to be able to retrieve
	// the collected spans.
	exporter, err := stdout.NewExporter(stdout.Options{PrettyPrint: true})
	if err != nil {
		log.Fatal(err)
	}

	// For the demonstration, use sdktrace.AlwaysSample sampler to sample all traces.
	// In a production application, use sdktrace.ProbabilitySampler with a desired probability.
	tp, err := sdktrace.NewProvider(sdktrace.WithConfig(sdktrace.Config{DefaultSampler: sdktrace.AlwaysSample()}),
		sdktrace.WithSyncer(exporter))
	if err != nil {
		log.Fatal(err)
	}
	global.SetTraceProvider(tp)
}

func TestTrace(t *testing.T) {
	flush, ctx, _, tr := SetupTestingTracer(t, RecorderTracer, 1.0, TestContextValues)
	defer flush()

	var res *http.Response

	err := tr.WithSpan(ctx, "Testing trace with span",
		func(ctx context.Context) error {
			var err error
			req, _ := http.NewRequest("GET", "https://example.com/test", nil)

			ctx, req = httptrace.W3C(ctx, req)
			httptrace.Inject(ctx, req)
			res, err = TestHTTPClient().Do(req)
			if err != nil {
				return err
			}

			SpanFromContext(ctx).SetStatus(codes.OK)

			return nil

		})

	ctxValues := res.Header.Get("Correlation-Context")
	pairs := strings.Split(ctxValues, ",")
	m := make(map[string]string)
	for _, kv := range TestContextValues {
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
