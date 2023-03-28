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
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registration"
	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/locks"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges/byterange"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

const testRangeBody = "This is a test file, to see how the byte range requests work.\n"

func newRangeRequestTestServer() *httptest.Server {

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Now(),
			strings.NewReader(testRangeBody))
	})
	s := httptest.NewServer(handler)
	return s
}

func TestInvalidContentRange(t *testing.T) {
	_, _, err := byterange.ParseContentRangeHeader("blah")
	if err == nil {
		t.Errorf("expected error: %s", `invalid input format`)
	}
}

func TestMultiPartByteRange(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, "62")
	resp2.Header.Add(headers.NameContentRange, "bytes 0-10/62")
	resp2.Header.Add("Content-Type", "multipart/byteranges; boundary=ddffee123")
	resp2.StatusCode = 200
	d := DocumentFromHTTPResponse(resp2, []byte("This is a t"), nil, testLogger)

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	ranges := make(byterange.Ranges, 1)
	ranges[0] = byterange.Range{Start: 5, End: 10}
	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": nil}, nil)
	if err != nil {
		t.Error("Expected multi part byte range request to pass, but failed with ", err.Error())
	}
}

func TestCacheHitRangeRequest(t *testing.T) {
	expected := "is a "
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.Itoa(len(testRangeBody)))
	resp2.StatusCode = 200
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody), nil, testLogger)
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

func TestCacheHitRangeRequest2(t *testing.T) {

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	have := byterange.Range{Start: 1, End: 20}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil, testLogger)
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

func TestCacheHitRangeRequest3(t *testing.T) {
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}
	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	have := byterange.Range{Start: 1, End: 20}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil, testLogger)
	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

func TestPartialCacheMissRangeRequest(t *testing.T) {
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	have := byterange.Range{Start: 1, End: 9}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil, testLogger)

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
	if err != nil {
		t.Error(err)
	}

	ranges := byterange.Ranges{byterange.Range{Start: 5, End: 20}}
	_, _, deltas, err := QueryCache(ctx, cache, "testKey", ranges, nil)
	if err != nil {
		t.Error(err)
	}
	if deltas == nil || len(deltas) < 1 {
		t.Errorf("invalid deltas: %v", deltas)
	} else if deltas[0].Start != 10 ||
		deltas[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, deltas[0].Start, deltas[0].End)
	}
}

func TestFullCacheMissRangeRequest(t *testing.T) {
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	have := byterange.Range{Start: 1, End: 9}
	cl := int64(len(testRangeBody))
	rl := (have.End - have.Start) + 1
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add(headers.NameContentLength, strconv.FormatInt(rl, 10))
	resp2.ContentLength = rl
	resp2.Header.Add(headers.NameContentRange, have.ContentRangeHeader(cl))
	resp2.StatusCode = 206
	d := DocumentFromHTTPResponse(resp2, []byte(testRangeBody[have.Start:have.End+1]), nil, testLogger)

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

func TestRangeRequestFromClient(t *testing.T) {

	want := byterange.Ranges{byterange.Range{Start: 15, End: 20}}
	haves := byterange.Ranges{byterange.Range{Start: 10, End: 25}}

	s := newRangeRequestTestServer()
	defer s.Close()
	client := &http.Client{}
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

	//--------------------------------------
	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, testLogger)
	cache, ok := caches["default"]
	if !ok {
		t.Error("could not load cache")
	}

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer()})

	d := DocumentFromHTTPResponse(resp, bytes, nil, testLogger)
	err = WriteCache(ctx, cache, "testKey2", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

func TestQueryCache(t *testing.T) {

	expected := "1234"

	conf, _, err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-provider", "test"})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf, testLogger)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	resp := &http.Response{}
	resp.Header = make(http.Header)
	resp.StatusCode = 200
	resp.Header.Add(headers.NameContentLength, "4")
	d := DocumentFromHTTPResponse(resp, []byte(expected), nil, testLogger)
	d.ContentType = "text/plain"

	ctx := context.Background()
	ctx = tc.WithResources(ctx, &request.Resources{BackendOptions: conf.Backends["default"], Tracer: tu.NewTestTracer(), Logger: testLogger})

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

	err = WriteCache(ctx, cache, "testKey", d, time.Duration(60)*time.Second, map[string]interface{}{"text/plain": true}, nil)
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

// Mock Cache for testing error conditions
type testCache struct {
	configuration *co.Options
	locker        locks.NamedLocker
}

func (tc *testCache) Connect() error {
	return nil
}

var errTest = errors.New("test error")

func (tc *testCache) Store(cacheKey string, data []byte, ttl time.Duration) error {
	return errTest
}

func (tc *testCache) Retrieve(cacheKey string, allowExpired bool) ([]byte, status.LookupStatus, error) {
	return nil, status.LookupStatusError, errTest
}

func (tc *testCache) SetTTL(cacheKey string, ttl time.Duration) {}
func (tc *testCache) Remove(cacheKey string)                    {}
func (tc *testCache) BulkRemove(cacheKeys []string)             {}
func (tc *testCache) Close() error                              { return errTest }
func (tc *testCache) Configuration() *co.Options                { return tc.configuration }
func (tc *testCache) Locker() locks.NamedLocker                 { return tc.locker }
func (tc *testCache) SetLocker(l locks.NamedLocker)             { tc.locker = l }
