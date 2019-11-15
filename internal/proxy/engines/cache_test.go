/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package engines

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
)

func init() {
	log.Printf("Running on :3000 ...")
	go http.ListenAndServe("localhost:3000",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "", time.Now(),
				strings.NewReader("This is a test file, to see how the byte range requests work.\n"))
		}))
}

func TestInvalidContentLength(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "blah")
	resp2.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, nil)
	if err == nil {
		t.Error(err)
	}
}

func TestInvalidContentLength2(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62foo")
	resp2.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, nil)
	if err == nil {
		t.Error(err)
	}
}

func TestInvalidContentRange(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62")
	resp2.Header.Add("Content-Range", "blah")
	resp2.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	ranges := make(model.Ranges, 1)
	ranges[0] = model.Range{Start: 5, End: 10}
	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, ranges)
	if err == nil {
		t.Error(err)
	}
}

func TestMultiPartByteRange(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62")
	resp2.Header.Add("Content-Range", "bytes 0-10/62")
	resp2.Header.Add("Content-Type", "multipart/byteranges; boundary=ddffee123")
	resp2.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	ranges := make(model.Ranges, 1)
	ranges[0] = model.Range{Start: 5, End: 10}
	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, ranges)
	if err != nil {
		t.Error("Expected multi part byte range request to pass, but failed with ", err.Error())
	}
}

func TestCacheHitRangeRequest(t *testing.T) {
	expected := "is a "
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62")
	resp2.StatusCode = 200
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, nil)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 5, End: 10}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}
	if (string(d2.Body[5:10])) != expected {
		t.Errorf("expected %s got %s", expected, string(d2.Body[5:10]))
	}
	if d2.UpdatedQueryRange != nil {
		t.Errorf("updated query range was expected to be empty")
	}
}

func TestCacheHitRangeRequest2(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62")
	resp2.StatusCode = 200
	resp2.Header.Add("Content-Range", "bytes 1-20/62")
	br := model.Range{Start: 1, End: 20}
	r := make(model.Ranges, 1)
	r[0] = br
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	err = WriteCache(cache, "testKey.sz", d, time.Duration(60)*time.Second, r)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 5, End: 10}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}

	if d2.UpdatedQueryRange != nil {
		t.Errorf("updated query range was expected to be empty")
	}
	if d2.Ranges[0].Start != 1 || d2.Ranges[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 1, 20, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestCacheHitRangeRequest3(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "62")
	resp2.StatusCode = 200
	resp2.Header.Add("Content-Range", "bytes 1-20/62")
	br := model.Range{Start: 1, End: 20}
	r := make(model.Ranges, 1)
	r[0] = br
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a test file, to see how the byte range requests work.\n"), nil)

	err = WriteCache(cache, "testKey.sz", d, time.Duration(60)*time.Second, r)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 25, End: 30}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, ranges)
	if err != nil {
		t.Error(err)
	}
	qrange := make(model.Ranges, 1)
	qrange[0] = model.Range{Start: 5, End: 10}
	d2, err := QueryCache(cache, "testKey", qrange)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange != nil {
		t.Error("Expected empty query range got non empty response ", d2.UpdatedQueryRange)
	}
}

func TestPartialCacheMissRangeRequest(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "10")
	resp2.Header.Add("Content-Range", " bytes 0-10/62")
	resp2.StatusCode = 206
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a "), nil)

	b := model.Range{Start: 0, End: 10}
	r := make(model.Ranges, 1)
	r[0] = b
	d.Ranges = r

	err = WriteCache(cache, "testKey.sz", d, time.Duration(60)*time.Second, r)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 5, End: 20}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange[0].Start != 10 ||
		d2.UpdatedQueryRange[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestFullCacheMissRangeRequest(t *testing.T) {
	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}
	resp2 := &http.Response{}
	resp2.Header = make(http.Header)
	resp2.Header.Add("Content-Length", "10")
	resp2.Header.Add("Content-Range", "bytes 0-10/62")
	resp2.StatusCode = 206
	d := model.DocumentFromHTTPResponse(resp2, []byte("This is a "), nil)

	b := model.Range{Start: 0, End: 10}
	r := make(model.Ranges, 1)
	r[0] = b
	d.Ranges = r

	err = WriteCache(cache, "testKey.sz", d, time.Duration(60)*time.Second, r)
	if err != nil {
		t.Error(err)
	}

	byteRange := model.Range{Start: 15, End: 20}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange[0].Start != 15 ||
		d2.UpdatedQueryRange[0].End != 20 {
		t.Errorf("expected start %d end %d, got start %d end %d", 10, 20, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestRangeRequestFromClient(t *testing.T) {
	client := &http.Client{}
	request, err := http.NewRequest("GET", "http://localhost:3000", nil)

	if err != nil {
		log.Fatalln(err)
	}
	request.Header.Set("Range", "bytes=10-25")
	resp, err := client.Do(request)

	bytes := make([]byte, resp.ContentLength)
	resp.Body.Read(bytes)
	fmt.Println(string(bytes))

	//--------------------------------------
	err = config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	d := model.DocumentFromHTTPResponse(resp, bytes, nil)
	r := model.GetByteRanges(request.Header.Get("Range"))
	err = WriteCache(cache, "testKey2.sz", d, time.Duration(60)*time.Second, r)
	byteRange := model.Range{Start: 15, End: 20}
	ranges := make(model.Ranges, 1)
	ranges[0] = byteRange
	d2, err := QueryCache(cache, "testKey2", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange != nil {
		t.Errorf("expected cache hit but got cache miss")
	}
	byteRange = model.Range{Start: 20, End: 35}
	ranges[0] = byteRange
	d2, err = QueryCache(cache, "testKey2", ranges)
	if err != nil {
		t.Error(err)
	}
	if d2.UpdatedQueryRange == nil {
		t.Errorf("expected cache miss but got cache hit")
	}
	if d2.UpdatedQueryRange[0].Start != 25 ||
		d2.UpdatedQueryRange[0].End != 35 {
		t.Errorf("expected start %d end %d, got start %d end %d", 25, 35, d2.UpdatedQueryRange[0].Start, d2.UpdatedQueryRange[0].End)
	}
}

func TestQueryCache(t *testing.T) {

	expected := "1234"

	err := config.Load("trickster", "test", []string{"-origin-url", "http://1", "-origin-type", "test"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
	}

	resp := &http.Response{}
	resp.Header = make(http.Header)
	resp.StatusCode = 200
	resp.Header.Add("Content-Length", "4")
	d := model.DocumentFromHTTPResponse(resp, []byte(expected), nil)

	err = WriteCache(cache, "testKey", d, time.Duration(60)*time.Second, nil)
	if err != nil {
		t.Error(err)
	}

	d2, err := QueryCache(cache, "testKey", nil)
	if err != nil {
		t.Error(err)
	}

	if string(d2.Body) != string(expected) {
		t.Errorf("expected %s got %s", string(expected), string(d2.Body))
	}

	if d2.StatusCode != 200 {
		t.Errorf("expected %d got %d", 200, d2.StatusCode)
	}

	_, err = QueryCache(cache, "testKey2", nil)
	if err == nil {
		t.Errorf("expected error")
	}
}
