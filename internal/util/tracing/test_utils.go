package tracing

import (
	"context"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/api/core"
	"go.opentelemetry.io/otel/api/distributedcontext"
	"go.opentelemetry.io/otel/api/key"
	"go.opentelemetry.io/otel/api/trace"
)

var (
	TestContextValues = []core.KeyValue{
		key.String("username", "guy"),
		key.Int("IntValue", 42),
	}
)

func MockRoundTripper(f func(r *http.Request) (*http.Response, error)) http.RoundTripper {
	return &rt{f}
}

type rt struct {
	f func(*http.Request) (*http.Response, error)
}

func (rt *rt) RoundTrip(r *http.Request) (*http.Response, error) {
	return rt.f(r)
}

var (
	TestEvents = []core.KeyValue{
		key.String("location", "testhandler"),
		key.Int("Integer Value", 1),
	}
)

func TestingHandler() http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, span := PrepareRequest(r, r.Host)
		defer span.End()

		func() {
			_, span := NewChildSpan(r.Context(), "test-span-name")
			defer span.End()
			span.AddEvent(r.Context(), "SubSpan", TestEvents[0])
		}()
		for i := 1; i < len(TestEvents); i++ {
			span.AddEvent(r.Context(), "Span", TestEvents[i])
		}
		_, _ = io.WriteString(w, "test response")

	})

}

func TestHTTPClient() *http.Client {
	client := http.Client{}
	w := httptest.NewRecorder()

	client.Transport = MockRoundTripper(

		func(req *http.Request) (*http.Response, error) {

			resp := &http.Response{
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				Header:     req.Header,
				Body:       ioutil.NopCloser(strings.NewReader("")),
				Request:    req,
			}
			TestingHandler().ServeHTTP(w, req)
			return resp, nil

		},
	)
	return &client

}

func SetupTestingTracer(t *testing.T, impl TracerImplementation, sampleRate float64, values []core.KeyValue) (flush func(), ctx context.Context, recorder *recorderExporter, tr trace.Tracer) {
	flush, recorder, err := setRecorderTracer(
		func(err error) {
			t.Error(err)
		},
		sampleRate,
	)
	if err != nil {
		t.Error(err)
	}

	ctx = distributedcontext.NewContext(context.Background(), values...)

	return flush,
		ctx,
		recorder,
		GlobalTracer(ctx)
}
