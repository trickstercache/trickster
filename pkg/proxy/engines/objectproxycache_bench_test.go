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

// 14787 ns/op		8609 B/op		123 allocs/op
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

	r.Header.Add(headers.NameRange, "bytes=9500-10000")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		ObjectProxyCacheRequest(w, r)
		//testFetchOPC(r, http.StatusPartialContent, "test", map[string]string{"status": "kmiss"})
	}
}
