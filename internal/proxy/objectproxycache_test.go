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

package proxy

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
	tu "github.com/Comcast/trickster/internal/util/testing"
)

func TestObjectProxyCacheRequest(t *testing.T) {

	es := tu.NewTestServer(200, "test")
	defer es.Close()

	err := config.Load("trickster", "test", []string{"-origin", es.URL, "-origin-type", "prometheus", "-log-level", "debug"})
	if err != nil {
		t.Errorf("Could not load configuration: %s", err.Error())
	}

	cr.LoadCachesFromConfig()
	cache, err := cr.GetCache("default")
	if err != nil {
		t.Error(err)
		return
	}

	client := TestClient{}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", es.URL, nil)

	// get URL

	req := NewRequest("default", "test", "TestProxyRequest", "GET", r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r)

	ObjectProxyCacheRequest(req, w, client, cache, 60, false, false) // client Client, cache cache.Cache, ttl int, refresh bool, noLock bool) {

	resp := w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "test" {
		t.Errorf("expected 'test' got '%s'.", bodyBytes)
	}

	// get cache hit coverage too by repeating:

	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", es.URL, nil)
	req = NewRequest("default", "test", "TestProxyRequest", "GET", r.URL, http.Header{"testHeaderName": []string{"testHeaderValue"}}, time.Duration(30)*time.Second, r)
	ObjectProxyCacheRequest(req, w, client, cache, 60, false, false) // client Client, cache cache.Cache, ttl int, refresh bool, noLock bool) {
	resp = w.Result()

	// it should return 200 OK
	if resp.StatusCode != 200 {
		t.Errorf("expected 200 got %d.", resp.StatusCode)
	}

	bodyBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Error(err)
	}

	if string(bodyBytes) != "test" {
		t.Errorf("expected 'test' got '%s'.", bodyBytes)
	}

}

// TestClient Implements Proxy Client Interface
type TestClient struct {
	Name   string
	User   string
	Pass   string
	Config config.OriginConfig
	Cache  cache.Cache
}

func (c TestClient) BaseURL() *url.URL {
	u := &url.URL{}
	return u
}
func (c TestClient) BuildUpstreamURL(r *http.Request) *url.URL {
	u := c.BaseURL()
	return u
}
func (c TestClient) Configuration() config.OriginConfig {
	return c.Config
}
func (c TestClient) OriginName() string {
	return c.Name
}
func (c TestClient) CacheInstance() cache.Cache {
	return c.Cache
}
func (c TestClient) DeriveCacheKey(r *Request, extra string) string {
	return md5.Checksum("test" + extra)
}
func (c TestClient) FastForwardURL(r *Request) (*url.URL, error) {
	u := c.BaseURL()
	return u, nil
}
func (c TestClient) HealthHandler(w http.ResponseWriter, r *http.Request) {}
func (c TestClient) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	return nil, nil
}
func (c TestClient) UnmarshalTimeseries(data []byte) (timeseries.Timeseries, error) {
	return nil, nil
}
func (c TestClient) UnmarshalInstantaneous(data []byte) (timeseries.Timeseries, error) {
	return nil, nil
}
func (c TestClient) ParseTimeRangeQuery(r *Request) (*timeseries.TimeRangeQuery, error) {
	trq := &timeseries.TimeRangeQuery{
		Statement: "up",
		Step:      60,
		Extent:    timeseries.Extent{Start: time.Unix(60000000, 0), End: time.Unix(120000000, 0)},
	}
	return trq, nil
}
func (c TestClient) RegisterRoutes(originName string, o config.OriginConfig) {}
func (c TestClient) SetExtent(r *Request, extent *timeseries.Extent)         {}
