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
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/prometheus"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/md5"
	"github.com/prometheus/common/model"
)

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

const sampleOutput1 = `{"status":"","data":{"resultType":"matrix","result":[` +
	`{"metric":{"__name__":"a"},"values":[[99,"1.5"],[199,"1.5"],[299,"1.5"]]},` +
	`{"metric":{"__name__":"b"},"values":[[99,"1.5"],[199,"1.5"],[299,"1.5"]]}]}}`

func (c TestClient) MarshalTimeseries(ts timeseries.Timeseries) ([]byte, error) {
	return []byte(sampleOutput1), nil
}

// Returns a fake object without actually unmarshaling anything
// this allows us to test the delta proxy cache
func (c TestClient) UnmarshalTimeseries(data []byte) (timeseries.Timeseries, error) {
	me := &prometheus.MatrixEnvelope{
		Data: prometheus.MatrixData{
			ResultType: "matrix",
			Result: model.Matrix{
				&model.SampleStream{
					Metric: model.Metric{"__name__": "a"},
					Values: []model.SamplePair{
						model.SamplePair{Timestamp: 99000, Value: 1.5},
						model.SamplePair{Timestamp: 199000, Value: 1.5},
						model.SamplePair{Timestamp: 299000, Value: 1.5},
					},
				},
				&model.SampleStream{
					Metric: model.Metric{"__name__": "b"},
					Values: []model.SamplePair{
						model.SamplePair{Timestamp: 99000, Value: 1.5},
						model.SamplePair{Timestamp: 199000, Value: 1.5},
						model.SamplePair{Timestamp: 299000, Value: 1.5},
					},
				},
			},
		},
	}
	return me, nil
}
func (c TestClient) UnmarshalInstantaneous(data []byte) (timeseries.Timeseries, error) {
	return nil, nil
}
func (c TestClient) ParseTimeRangeQuery(r *Request) (*timeseries.TimeRangeQuery, error) {
	if r.HandlerName == "TestProxyRequestParseError" {
		return nil, fmt.Errorf("simulated ParseTimeRangeQuery error")
	}

	trq := &timeseries.TimeRangeQuery{
		Statement: "up",
		Step:      60,
		Extent:    timeseries.Extent{Start: time.Unix(60000000, 0), End: time.Unix(120000000, 0)},
	}
	return trq, nil
}
func (c TestClient) RegisterRoutes(originName string, o config.OriginConfig) {}
func (c TestClient) SetExtent(r *Request, extent *timeseries.Extent)         {}
