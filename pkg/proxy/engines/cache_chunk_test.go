/*
 * Copyright 2018 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package engines

import (
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"testing"
	"time"

	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestMultiPartByteRangeChunks(t *testing.T) {
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, "62")
	resp2.Header.Add(headers.NameContentRange, "bytes 0-10/62")
	resp2.Header.Add("Content-Type", "multipart/byteranges; boundary=ddffee123")
	resp2.StatusCode = 200
	d := DocumentFromHTTPResponse(resp2, []byte("This is a t"), nil)

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	ranges := make(byterange.Ranges, 1)
	ranges[0] = byterange.Range{Start: 5, End: 10}
	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error("Expected multi part byte range request to pass, but failed with ", err.Error())
	}
}

func TestCacheHitRangeRequestChunks(t *testing.T) {
	logger.SetLogger(testLogger)
	expected := "is a "
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.Itoa(len(testRangeBody)))
	resp2.StatusCode = 200
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody), nil)
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	ranges := byterange.Ranges{byterange.Range{Start: 5, End: 10}}
	d2, _, deltas, err := QueryCache(ctx, cache, "testKey", ranges, nil)
	if err != nil {
		t.Error(err)
	}
	if (string(d2.Body[5:10])) != expected {
		t.Errorf("expected %s got %s", expected, string(d2.Body[5:10]))
	}
	if deltas != nil {
		t.Errorf("updated query range was expected to be empty")
	}
}

func TestCacheHitRangeRequest2Chunks(t *testing.T) {
	logger.SetLogger(testLogger)
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	have := byterange.Range{Start: 1, End: 20}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil)
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	ranges := byterange.Ranges{byterange.Range{Start: 5, End: 10}}
	d2, _, deltas, err := QueryCache(ctx, cache, "testKey", ranges, nil)
	if err != nil {
		t.Error(err)
	}

	if len(deltas) > 0 {
		t.Errorf("updated query range was expected to be empty: %v", deltas)
	}
	if d2.Ranges[0].Start != 1 || d2.Ranges[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 1, 20, deltas[0].Start, deltas[0].End)
	}
}

func TestCacheHitRangeRequest3Chunks(t *testing.T) {
	logger.SetLogger(testLogger)
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	have := byterange.Range{Start: 1, End: 20}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil)
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	qrange := byterange.Ranges{byterange.Range{Start: 5, End: 10}}
	_, _, deltas, err := QueryCache(ctx, cache, "testKey", qrange, nil)
	if err != nil {
		t.Error(err)
	}
	if len(deltas) > 0 {
		t.Error("Expected empty query range got non empty response ", deltas)
	}
}

func TestPartialCacheMissRangeRequestChunks(t *testing.T) {
	logger.SetLogger(testLogger)
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	have := byterange.Range{Start: 1, End: 9}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil)

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	ranges := byterange.Ranges{byterange.Range{Start: 5, End: 20}}
	_, _, deltas, err := QueryCache(ctx, cache, "testKey", ranges, nil)
	if err != nil {
		t.Error(err)
	}
	if len(deltas) == 0 {
		t.Errorf("invalid deltas: %v", deltas)
	} else if deltas[0].Start != 10 ||
		deltas[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, deltas[0].Start, deltas[0].End)
	}
}

func TestFullCacheMissRangeRequestChunks(t *testing.T) {
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	have := byterange.Range{Start: 1, End: 9}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil)

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	ranges := byterange.Ranges{byterange.Range{Start: 15, End: 20}}
	_, _, deltas, err := QueryCache(ctx, cache, "testKey", ranges, nil)
	if err != nil {
		t.Error(err)
	}
	if deltas[0].Start != 15 ||
		deltas[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, deltas[0].Start, deltas[0].End)
	}
}

func TestRangeRequestFromClientChunks(t *testing.T) {
	logger.SetLogger(testLogger)
	want := byterange.Ranges{byterange.Range{Start: 15, End: 20}}
	haves := byterange.Ranges{byterange.Range{Start: 10, End: 25}}

	s := newRangeRequestTestServer()
	transport := &http.Transport{}
	client := &http.Client{Transport: transport}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		s.Close()
	})
	req, err := http.NewRequest(http.MethodGet, s.URL, nil)
	if err != nil {
		log.Fatalln(err)
	}
	req.Header.Set(headers.NameRange, haves.String())
	resp, err := client.Do(req)
	if err != nil {
		t.Error(err)
	}

	bytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	//--------------------------------------
	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	d := DocumentFromHTTPResponse(resp, bytes, nil)
	err = WriteCache(ctx, cache, "testKey2", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}
	_, _, deltas, err := QueryCache(ctx, cache, "testKey2", want, nil)
	if err != nil {
		t.Error(err)
	}
	if len(deltas) > 0 {
		t.Errorf("expected cache hit but got cache miss: %s", deltas)
	}
	want[0].Start = 20
	want[0].End = 35
	_, _, deltas, err = QueryCache(ctx, cache, "testKey2", want, nil)
	if err != nil {
		t.Error(err)
	}
	if deltas == nil {
		t.Errorf("expected cache miss but got cache hit")
	}
	if deltas[0].Start != 26 || deltas[0].End != 35 {
		t.Errorf("expected start %d end %d, got start %d end %d", 26, 35, deltas[0].Start, deltas[0].End)
	}
}

func TestQueryCacheChunks(t *testing.T) {
	logger.SetLogger(testLogger)
	expected := "1234"

	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}
	cache.Configuration().UseCacheChunking = true

	resp := &http.Response{}
	resp.Header = make(http.Header)
	resp.StatusCode = 200
	resp.Header.Add(headers.NameContentLength, "4")
	d := DocumentFromHTTPResponse(resp, []byte(expected), nil)
	d.ContentType = headers.ValueTextPlain

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	d2, _, _, err := QueryCache(ctx, cache, "testKey", nil, nil)
	if err != nil {
		t.Error(err)
	}

	if string(d2.Body) != expected {
		t.Errorf("expected %s got %s", expected, string(d2.Body))
	}

	if d2.StatusCode != 200 {
		t.Errorf("expected %d got %d", 200, d2.StatusCode)
	}

	_, _, _, err = QueryCache(ctx, cache, "testKey2", nil, nil)
	if err == nil {
		t.Errorf("expected error")
	}

	// test marshaling route by making our cache not appear to be a memory cache
	cache.Remove("testKey")
	cache.Configuration().Provider = "test"

	_, _, _, err = QueryCache(ctx, cache, "testKey", byterange.Ranges{{Start: 0, End: 1}}, nil)
	if err == nil {
		t.Errorf("expected error")
	}

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Error(err)
	}

	d2, _, _, err = QueryCache(ctx, cache, "testKey", nil, nil)
	if err != nil {
		t.Error(err)
	}

	if string(d2.Body) != expected {
		t.Errorf("expected %s got %s", expected, string(d2.Body))
	}

	if d2.StatusCode != 200 {
		t.Errorf("expected %d got %d", 200, d2.StatusCode)
	}
}

// TestTimeseriesChunkIteratorBoundary verifies chunk iteration with exact
// boundary alignment (extent fits exactly into chunk boundaries).
func TestTimeseriesChunkIteratorBoundary(t *testing.T) {
	step := 10 * time.Second
	csize := step * 5 // 50s per chunk

	start := time.Unix(0, 0)
	end := time.Unix(100, 0) // exactly 2 chunks

	iter := &TimeseriesChunkQueryIterator{
		key:   "test",
		cext:  timeseries.Extent{Start: start, End: end},
		csize: csize,
		trq:   &timeseries.TimeRangeQuery{Step: step},
	}

	var count int
	iter.IterateChunks(func(i int, subkey string) bool {
		count++
		if subkey == "" {
			t.Errorf("chunk %d: empty subkey", i)
		}
		return true
	})

	// [0,50) and [50,100) = 2 chunks
	if count != 2 {
		t.Errorf("expected 2 chunks, got %d", count)
	}
}

// TestTimeseriesChunkIteratorEarlyStop verifies that returning false from
// the callback stops iteration.
func TestTimeseriesChunkIteratorEarlyStop(t *testing.T) {
	step := 10 * time.Second
	csize := step * 5

	iter := &TimeseriesChunkQueryIterator{
		key:   "test",
		cext:  timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(200, 0)},
		csize: csize,
		trq:   &timeseries.TimeRangeQuery{Step: step},
	}

	var count int
	iter.IterateChunks(func(i int, subkey string) bool {
		count++
		return count < 2 // stop after 2 iterations
	})

	if count != 2 {
		t.Errorf("expected iteration to stop at 2, got %d", count)
	}
}

// TestByterangeChunkIteratorSingleChunk verifies edge case where the
// entire range fits within a single chunk.
func TestByterangeChunkIteratorSingleChunk(t *testing.T) {
	iter := &ByterangeChunkQueryIterator{
		key:  "test",
		crs:  0,
		cre:  1024,
		size: 4096, // chunk size larger than range
	}

	var count int
	var keys []string
	iter.IterateChunks(func(i int, subkey string) bool {
		count++
		keys = append(keys, subkey)
		return true
	})

	// range [0, 1024) with 4096 chunk size = only crs(0) < cre(1024), so 1 chunk
	if count != 1 {
		t.Errorf("expected 1 chunk for single-chunk range, got %d", count)
	}
}

// TestByterangeChunkIteratorMultiple verifies iteration over multiple byte range chunks.
func TestByterangeChunkIteratorMultiple(t *testing.T) {
	iter := &ByterangeChunkQueryIterator{
		key:  "test",
		crs:  0,
		cre:  3072,
		size: 1024,
	}

	var count int
	iter.IterateChunks(func(i int, subkey string) bool {
		count++
		return true
	})

	// [0, 1024), [1024, 2048), [2048, 3072) = 3 chunks
	if count != 3 {
		t.Errorf("expected 3 chunks, got %d", count)
	}
}

// TestChunkWriteAndReadRoundtrip verifies that writing chunks and reading
// them back produces the same data.
func TestChunkWriteAndReadRoundtrip(t *testing.T) {
	logger.SetLogger(testLogger)
	expected := "the quick brown fox jumps over the lazy dog"

	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Fatal("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{headers.NameContentLength: {strconv.Itoa(len(expected))}},
	}
	d := DocumentFromHTTPResponse(resp, []byte(expected), nil)
	d.ContentType = headers.ValueTextPlain

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "roundtrip-key", d, 60*time.Second, sets.New([]string{headers.ValueTextPlain}), nil)
	if err != nil {
		t.Fatalf("WriteCache: %v", err)
	}

	d2, _, _, err := QueryCache(ctx, cache, "roundtrip-key", nil, nil)
	if err != nil {
		t.Fatalf("QueryCache: %v", err)
	}

	if string(d2.Body) != expected {
		t.Errorf("roundtrip mismatch: expected %q, got %q", expected, string(d2.Body))
	}
	if d2.StatusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, d2.StatusCode)
	}
}

// TestChunkQueryMissingKey verifies that querying chunks for a key that
// doesn't exist returns an appropriate error.
func TestChunkQueryMissingKey(t *testing.T) {
	logger.SetLogger(testLogger)

	conf, err := config.Load([]string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Fatal("could not load cache")
	}
	cache.Configuration().UseCacheChunking = true

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	_, _, _, err = QueryCache(ctx, cache, "nonexistent-key", nil, nil)
	if err == nil {
		t.Error("expected error for nonexistent key, got nil")
	}
}
