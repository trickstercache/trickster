package tracing

import (
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"

	"go.opentelemetry.io/otel/api/key"
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

func TestingHandler() http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, span := PrepareRequest(r, r.Host)
		defer span.End()

		func() {
			_, span := NewChildSpan(r.Context(), "test-span-name")
			defer span.End()
			span.AddEvent(r.Context(), "SubSpan", key.String("location", "testhandler"))
		}()

		span.AddEvent(r.Context(), "Span", key.Int("Integer Value", 1))
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
