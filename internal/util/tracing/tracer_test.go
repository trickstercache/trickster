package tracing

import (
	"context"
	"fmt"
	"io/ioutil"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/api/global"
	"google.golang.org/grpc/codes"
)

func TestHTTPtoCode(t *testing.T) {
	tests := []struct {
		start int
		end   int // exclusive
		code  codes.Code
	}{
		{
			100,
			399,
			codes.OK,
		},
		{
			404,
			405,
			codes.NotFound,
		},
		{
			400,
			404,
			codes.InvalidArgument,
		},
		{
			405,
			500,
			codes.InvalidArgument,
		},
		{
			503,
			504,
			codes.Unavailable,
		},
		{
			500,
			503,
			codes.Internal,
		},
		{
			504,
			600,
			codes.Internal,
		},
	}
	for _, test := range tests {
		for i := test.start; i < test.end; i++ {
			code := HTTPToCode(i)

			assert.Equalf(t,
				test.code,
				code,
				"HTTP status code is %v and Code is %v. Code should be %v", i, code.String(), test.code.String(),
			)
		}
	}
}

func TestTracingMiddleware(t *testing.T) {

	TraceExporters["unknown-exporter"] = -1
	TracerImplementations["unknown-tracer"] = -1

	for name, ex := range TraceExporters {
		for tracerName, tracer := range TracerImplementations {
			details := fmt.Sprintf("Tracer=%s(%d):Exporter=%s(%d)", tracer.String(), tracer, ex.String(), ex)
			tr, flush, r, err := SetTracer(tracer, ex, WithCollector("http://example/com"), WithSampleRate(1))
			assert.NoError(t, err, "failed to setup tracer")
			assert.Equal(t, ex.String(), name, details)
			assert.Equal(t, tracer.String(), tracerName, details)
			newTR := global.TraceProvider().Tracer(tracerName)
			assert.NotNil(t, newTR, "Nil global tracer", details)
			if ex == RecorderExporter {
				assert.NotNil(t, r, "Nil recorder")
				ctx, span := tr.Start(
					context.Background(),
					"Request",
				)
				span.AddEvent(ctx, "Test Span Event")
				span.End()
				if r == nil {
					t.Log("nil recorder")
				}
				if r.buf == nil {
					t.Log("nil recorder buffer")
				}

				b, err := ioutil.ReadAll(r)
				assert.NoError(t, err, "Failed to read span recorder")
				t.Logf("%s", b)
			}
			flush()
		}
	}
}
