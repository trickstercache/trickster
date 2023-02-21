package engines

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// 39669 ns/op		57914 B/op		101 allocs/op
func BenchmarkObjectProxyCache(b *testing.B) {
	license, err := os.Open("../../../LICENSE")
	if err != nil {
		b.Fatal(err)
	}
	body, err := io.ReadAll(license)
	if err != nil {
		b.Fatal(err)
	}
	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", string(body), http.StatusPartialContent, hdrs)
	if err != nil {
		b.Error(err)
	}
	defer ts.Close()

	r.Header.Add(headers.NameRange, "bytes=0-10000")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		ObjectProxyCacheRequest(w, r)
	}
}

// 48449 ns/op		68814 B/op		187 allocs/op
func BenchmarkObjectProxyCacheChunks(b *testing.B) {
	license, err := os.Open("../../../LICENSE")
	if err != nil {
		b.Fatal(err)
	}
	body, err := io.ReadAll(license)
	if err != nil {
		b.Fatal(err)
	}
	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", string(body), http.StatusPartialContent, hdrs)
	if err != nil {
		b.Error(err)
	}
	rsc.CacheConfig.UseCacheChunking = true
	defer ts.Close()

	r.Header.Add(headers.NameRange, "bytes=0-10000")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		ObjectProxyCacheRequest(w, r)
	}
}
