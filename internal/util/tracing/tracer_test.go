package tracing

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func TestTracingMiddleware(t *testing.T) {

	impls := make(map[string]TracerImplementation)
	for k, v := range TracerImplementations {
		impls[k] = v
	}

	impls["unknown-tracer"] = -1

	for name, impl := range impls {
		flush, err := SetTracer(impl, "http://example/com", 1.0)
		assert.NoError(t, err, "failed to setup tracer")

		ctx := MiddlewarePassthrough()

		assert.Equal(t, impl.String(), name)
		newTR := GlobalTracer(ctx)
		assert.NotNil(t, newTR, "Nil global tracer")
		flush()
	}

}

func MiddlewarePassthrough() context.Context {
	var (
		req = httptest.NewRequest(http.MethodGet, "http://example.com/foo", new(bytes.Buffer))
		w   = httptest.NewRecorder()
	)
	grabber := &ctxGrabber{}
	mware := Trace()(grabber)
	mware.ServeHTTP(w, req)
	return grabber.ctx

}

type ctxGrabber struct {
	ctx context.Context
}

func (c *ctxGrabber) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.ctx = r.Context()
}

func Trace() mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			r, span := PrepareRequest(r, r.Host) // TODO Host is not the best tracer name. Something Request level would be better, but paths are already in the trace

			defer span.End()

			next.ServeHTTP(w, r)
		})
	}
}
