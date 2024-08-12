package writer

import "net/http"

func NewWriter() http.ResponseWriter {
	return &TestResponseWriter{
		Headers: make(http.Header),
		Bytes:   make([]byte, 0, 8192),
	}
}

type TestResponseWriter struct {
	Headers    http.Header
	StatusCode int
	Bytes      []byte
}

func (w *TestResponseWriter) Header() http.Header {
	return w.Headers
}

func (w *TestResponseWriter) WriteHeader(statusCode int) {
	w.StatusCode = statusCode
}

func (w *TestResponseWriter) Write(b []byte) (int, error) {
	w.Bytes = append(w.Bytes, b...)
	return len(b), nil
}

func (w *TestResponseWriter) Reset() {
	w.Headers = make(http.Header)
	w.StatusCode = 0
	w.Bytes = make([]byte, 0, 8192)
}
