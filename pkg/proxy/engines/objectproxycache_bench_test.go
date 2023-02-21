package engines

import (
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// 14787 ns/op		8609 B/op		123 allocs/op
func BenchmarkObjectProxyCache(b *testing.B) {
	hdrs := map[string]string{"Cache-Control": "max-age=60"}
	ts, _, r, rsc, err := setupTestHarnessOPC("", "test", http.StatusPartialContent, hdrs)
	if err != nil {
		b.Error(err)
	}
	defer ts.Close()

	r.Header.Add(headers.NameRange, "bytes=0-3")

	o := rsc.BackendOptions
	o.MaxTTLMS = 15000
	o.MaxTTL = time.Duration(o.MaxTTLMS) * time.Millisecond

	for i := 0; i < b.N; i++ {
		testFetchOPC(r, http.StatusPartialContent, "test", map[string]string{"status": "kmiss"})
	}
}
