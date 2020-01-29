package tracing

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
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

	impls := make(map[string]TracerImplementation)
	for k, v := range TracerImplementations {
		impls[k] = v
	}

	impls["unknown-tracer"] = -1

	for name, impl := range impls {
		_, flush, r, err := SetTracer(impl, "http://example/com", 1.0)
		assert.NoError(t, err, "failed to setup tracer")
		assert.Equal(t, impl.String(), name)
		newTR := global.TraceProvider().Tracer(name)
		assert.NotNil(t, newTR, "Nil global tracer")
		flush()
		if r != nil && r.errorFunc != nil {
			// cover the error func call
			r.errorFunc(errors.New("dummy error"))
		}
	}

}

func MiddlewarePassthrough() context.Context {
	var (
		req = httptest.NewRequest(http.MethodGet, "http://example.com/foo", new(bytes.Buffer))
		w   = httptest.NewRecorder()
	)
	grabber := &ctxGrabber{}
	mware := testTrace()(grabber)
	mware.ServeHTTP(w, req)
	return grabber.ctx

}

type ctxGrabber struct {
	ctx context.Context
}

func (c *ctxGrabber) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.ctx = r.Context()
}

func testTrace() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tr := global.TraceProvider().Tracer("noop")
			r, span := PrepareRequest(r, tr)
			defer span.End()
			next.ServeHTTP(w, r)
		})
	}
}
