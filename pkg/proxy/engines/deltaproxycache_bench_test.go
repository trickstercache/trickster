package engines

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// 131075 ns/op		81103 B/op		2327 allocs/op
func BenchmarkDeltaProxyCache(b *testing.B) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		b.Error(err)
	}
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		client.QueryRangeHandler(w, r)
	}
}

// 137204 ns/op		84277 B/op		2367 allocs/op
func BenchmarkDeltaProxyCacheChunks(b *testing.B) {
	ts, _, r, rsc, err := setupTestHarnessDPC()
	if err != nil {
		b.Error(err)
	}
	rsc.CacheConfig.UseCacheChunking = true
	defer ts.Close()

	client := rsc.BackendClient.(*TestClient)
	o := rsc.BackendOptions

	o.FastForwardDisable = true
	step := time.Duration(300) * time.Second

	now := time.Now()
	end := now.Add(-time.Duration(12) * time.Hour)

	extr := timeseries.Extent{Start: end.Add(-time.Duration(18) * time.Hour), End: end}

	u := r.URL
	u.Path = "/prometheus/api/v1/query_range"
	u.RawQuery = fmt.Sprintf("step=%d&start=%d&end=%d&query=%s",
		int(step.Seconds()), extr.Start.Unix(), extr.End.Unix(), queryReturnsOKNoLatency)

	w := httptest.NewRecorder()
	for i := 0; i < b.N; i++ {
		client.QueryRangeHandler(w, r)
	}
}
