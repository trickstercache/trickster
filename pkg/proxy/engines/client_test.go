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
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	tst "github.com/trickstercache/trickster/v2/pkg/testutil/timeseries/model"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"
	tt "github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

// Prometheus API
const (
	APIPath         = "/api/v1/"
	mnQueryRange    = "query_range"
	mnQuery         = "query"
	mnLabels        = "labels"
	mnLabel         = "label"
	mnSeries        = "series"
	mnTargets       = "targets"
	mnRules         = "rules"
	mnAlerts        = "alerts"
	mnAlertManagers = "alertmanagers"
	mnStatus        = "status"
)

// Common URL Parameter Names
const (
	upQuery = "query"
	upStart = "start"
	upEnd   = "end"
	upStep  = "step"
	upTime  = "time"
	upMatch = "match[]"
)

// Client Implements Proxy Client Interface
type TestClient struct {
	backends.TimeseriesBackend

	fftime          time.Time
	InstantCacheKey string
	RangeCacheKey   string
}

func NewTestClient(name string, o *bo.Options, router http.Handler,
	cache cache.Cache, modeler *timeseries.Modeler) (backends.TimeseriesBackend, error) {

	c := &TestClient{}
	b, err := backends.NewTimeseriesBackend(name, o, c.RegisterHandlers, router, cache, modeler)
	c.TimeseriesBackend = b
	return c, err
}

func (c *TestClient) RegisterHandlers(map[string]http.Handler) {
	c.TimeseriesBackend.RegisterHandlers(
		map[string]http.Handler{
			"health":     http.HandlerFunc(c.HealthHandler),
			mnQueryRange: http.HandlerFunc(c.QueryRangeHandler),
			mnQuery:      http.HandlerFunc(c.QueryHandler),
			mnSeries:     http.HandlerFunc(c.SeriesHandler),
			"proxycache": http.HandlerFunc(c.QueryHandler),
			"proxy":      http.HandlerFunc(c.ProxyHandler),
		},
	)
}

// DefaultPathConfigs returns the default PathConfigs for the given Provider
func (c *TestClient) DefaultPathConfigs(o *bo.Options) map[string]*po.Options {

	paths := map[string]*po.Options{

		APIPath + mnQueryRange: {
			Path:            APIPath + mnQueryRange,
			HandlerName:     mnQueryRange,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upStep},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			ResponseHeaders: map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d",
				headers.ValueSharedMaxAge, 86400)},
		},

		APIPath + mnQuery: {
			Path:            APIPath + mnQuery,
			HandlerName:     mnQuery,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upQuery, upTime},
			CacheKeyHeaders: []string{headers.NameAuthorization},
			ResponseHeaders: map[string]string{headers.NameCacheControl: fmt.Sprintf("%s=%d",
				headers.ValueSharedMaxAge, 30)},
		},

		APIPath + mnSeries: {
			Path:            APIPath + mnSeries,
			HandlerName:     mnSeries,
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{upMatch, upStart, upEnd},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnLabels: {
			Path:            APIPath + mnLabels,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet, http.MethodPost},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnLabel: {
			Path:            APIPath + mnLabel,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnTargets: {
			Path:            APIPath + mnTargets,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnRules: {
			Path:            APIPath + mnRules,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnAlerts: {
			Path:            APIPath + mnAlerts,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnAlertManagers: {
			Path:            APIPath + mnAlertManagers,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath + mnStatus: {
			Path:            APIPath + mnStatus,
			HandlerName:     "proxycache",
			Methods:         []string{http.MethodGet},
			CacheKeyParams:  []string{},
			CacheKeyHeaders: []string{headers.NameAuthorization},
		},

		APIPath: {
			Path:        APIPath,
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		},

		"/opc": {
			Path:        "/opc",
			HandlerName: "proxycache",
			Methods:     []string{http.MethodGet},
		},

		"/": {
			Path:        "/",
			HandlerName: "proxy",
			Methods:     []string{http.MethodGet, http.MethodPost},
		},
	}

	o.Paths = paths
	o.FastForwardPath = paths[APIPath+mnQuery]

	return paths

}

// parseTime converts a query time URL parameter to time.Time.
// Copied from https://github.com/prometheus/prometheus/blob/master/web/api/v1/api.go
func parseTime(s string) (time.Time, error) {
	if t, err := strconv.ParseFloat(s, 64); err == nil {
		s, ns := math.Modf(t)
		ns = math.Round(ns*1000) / 1000
		return time.Unix(int64(s), int64(ns*float64(time.Second))), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cannot parse %q to a valid timestamp", s)
}

// parseDuration parses prometheus step parameters, which can be float64 or durations like 1d, 5m, etc
// the proxy.ParseDuration handles the second kind, and the float64's are handled here
func parseDuration(input string) (time.Duration, error) {
	v, err := strconv.ParseFloat(input, 64)
	if err != nil {
		return tt.ParseDuration(input)
	}
	// assume v is in seconds
	return time.Duration(int64(v)) * time.Second, nil
}

// ParseTimeRangeQuery parses the key parts of a TimeRangeQuery from the inbound HTTP Request
func (c *TestClient) ParseTimeRangeQuery(r *http.Request) (*timeseries.TimeRangeQuery,
	*timeseries.RequestOptions, bool, error) {

	trq := &timeseries.TimeRangeQuery{Extent: timeseries.Extent{}}
	rlo := &timeseries.RequestOptions{}
	qp, _, _ := params.GetRequestValues(r)

	trq.Statement = qp.Get(upQuery)
	if trq.Statement == "" {
		return nil, nil, false, errors.MissingURLParam(upQuery)
	}

	if p := qp.Get(upStart); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, nil, false, err
		}
		trq.Extent.Start = t
	} else {
		return nil, nil, false, errors.MissingURLParam(upStart)
	}

	if p := qp.Get(upEnd); p != "" {
		t, err := parseTime(p)
		if err != nil {
			return nil, nil, false, err
		}
		trq.Extent.End = t
	} else {
		return nil, nil, false, errors.MissingURLParam(upEnd)
	}

	if p := qp.Get(upStep); p != "" {
		step, err := parseDuration(p)
		if err != nil {
			return nil, nil, false, err
		}
		trq.Step = step
	} else {
		return nil, nil, false, errors.MissingURLParam(upStep)
	}

	if strings.Contains(trq.Statement, " offset ") {
		trq.IsOffset = true
		rlo.FastForwardDisable = true
	}

	if strings.Contains(trq.Statement, timeseries.FastForwardUserDisableFlag) {
		rlo.FastForwardDisable = true
	}

	return trq, rlo, true, nil
}

// BaseURL returns a URL in the form of scheme://host/path based on the proxy configuration
func (c *TestClient) BaseURL() *url.URL {
	o := c.Configuration()
	u := &url.URL{}
	u.Scheme = o.Scheme
	u.Host = o.Host
	u.Path = o.PathPrefix
	return u
}

// BuildUpstreamURL will merge the downstream request with the BaseURL to construct the full upstream URL
func (c *TestClient) BuildUpstreamURL(r *http.Request) *url.URL {
	u := c.BaseURL()

	if strings.HasPrefix(r.URL.Path, "/"+c.Name()+"/") {
		u.Path += strings.Replace(r.URL.Path, "/"+c.Name()+"/", "/", 1)
	} else {
		u.Path += r.URL.Path
	}

	u.RawQuery = r.URL.RawQuery
	u.Fragment = r.URL.Fragment
	u.User = r.URL.User
	return u
}

// SetExtent will change the upstream request query to use the provided Extent
func (c *TestClient) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {
	v, _, _ := params.GetRequestValues(r)
	v.Set(upStart, strconv.FormatInt(extent.Start.Unix(), 10))
	v.Set(upEnd, strconv.FormatInt(extent.End.Unix(), 10))
	params.SetRequestValues(r, v)
}

// FastForwardRequest returns an *http.Request crafted to collect Fast Forward
// data from the Origin, based on the provided HTTP Request
func (c *TestClient) FastForwardRequest(r *http.Request) (*http.Request, error) {
	nr := r.Clone(context.Background())
	u := nr.URL
	if strings.HasSuffix(u.Path, "/query_range") {
		u.Path = u.Path[0 : len(u.Path)-6]
	}

	// let the test client have a way to throw an error
	if strings.Contains(u.RawQuery, "throw_ffurl_error=1") {
		return nil, fmt.Errorf("This is an intentional test error: %s", ":)")
	}

	if strings.HasSuffix(nr.URL.Path, "/query_range") {
		nr.URL.Path = nr.URL.Path[0 : len(nr.URL.Path)-6]
	}
	v, _, _ := params.GetRequestValues(nr)
	v.Del(upStart)
	v.Del(upEnd)
	v.Del(upStep)

	if c.fftime.IsZero() {
		c.fftime = time.Now()
	}
	v.Set("time", strconv.FormatInt(c.fftime.Unix(), 10))

	params.SetRequestValues(nr, v)
	return nr, nil
}

// // VectorEnvelope represents a Vector response object from the Prometheus HTTP API
// type VectorEnvelope struct {
// 	Status string     `json:"status"`
// 	Data   VectorData `json:"data"`
// }

// // VectorData represents the Data body of a Vector response object from the Prometheus HTTP API
// type VectorData struct {
// 	ResultType string       `json:"resultType"`
// 	Result     model.Vector `json:"result"`
// }

// // MatrixEnvelope represents a Matrix response object from the Prometheus HTTP API
// type MatrixEnvelope struct {
// 	Status       string                `json:"status"`
// 	Data         MatrixData            `json:"data"`
// 	ExtentList   timeseries.ExtentList `json:"extents,omitempty"`
// 	StepDuration time.Duration         `json:"step,omitempty"`

// 	timestamps map[time.Time]bool // tracks unique timestamps in the matrix data
// 	tslist     times.Times
// 	isSorted   bool // tracks if the matrix data is currently sorted
// 	isCounted  bool // tracks if timestamps slice is up-to-date

// 	timeRangeQuery *timeseries.TimeRangeQuery
// }

// // MatrixData represents the Data body of a Matrix response object from the Prometheus HTTP API
// type MatrixData struct {
// 	ResultType string       `json:"resultType"`
// 	Result     model.Matrix `json:"result"`
// }

func (c *TestClient) marshalTimeseriesWriter(ts timeseries.Timeseries, w io.Writer) error {
	// Marshal the Envelope back to a json object for Cache Storage
	if c.RangeCacheKey == "failkey" {
		return fmt.Errorf("generic failure for testing purposes (key: %s)", c.RangeCacheKey)
	}

	ds, ok := ts.(*dataset.DataSet)
	if !ok {
		return timeseries.ErrUnknownFormat
	}
	// With Prometheus we presume only one Result per Dataset
	if len(ds.Results) != 1 {
		return timeseries.ErrUnknownFormat
	}

	w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[`)) // todo: always "success" ?

	seriesSep := ""
	for _, s := range ds.Results[0].SeriesList {
		w.Write([]byte(seriesSep + `{"metric":{`))
		sep := ""
		for _, k := range s.Header.Tags.Keys() {
			w.Write([]byte(fmt.Sprintf(`%s"%s":"%s"`, sep, k, s.Header.Tags[k])))
			sep = ","
		}
		w.Write([]byte(`},"values":[`))
		sep = ""
		sort.Sort(s.Points)
		for _, p := range s.Points {
			w.Write([]byte(fmt.Sprintf(`%s[%s,"%s"]`,
				sep,
				strconv.FormatFloat(float64(p.Epoch)/1000000000, 'f', -1, 64),
				p.Values[0]),
			))
			sep = ","
		}
		w.Write([]byte("]}"))
		seriesSep = ","
	}
	w.Write([]byte("]}}"))
	return nil

}

func (c *TestClient) testModeler() *timeseries.Modeler {
	m := tst.Modeler()
	mw := m.WireMarshalWriter
	m.WireMarshalWriter = func(ts timeseries.Timeseries, rlo *timeseries.RequestOptions, status int, w io.Writer) error {
		if c.RangeCacheKey == "failkey" {
			return fmt.Errorf("generic failure for testing purposes (key: %s)", c.RangeCacheKey)
		}
		return mw(ts, rlo, status, w)
	}
	return m
}

func (c *TestClient) HealthHandler(w http.ResponseWriter, r *http.Request) {
	u := c.BaseURL()
	u.Path += APIPath + mnLabels
	DoProxy(w, r, true)
}

func (c *TestClient) QueryRangeHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = c.BuildUpstreamURL(r)
	DeltaProxyCacheRequest(w, r, tst.Modeler())
}

func (c *TestClient) QueryHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = c.BuildUpstreamURL(r)
	ObjectProxyCacheRequest(w, r)
}

func (c *TestClient) SeriesHandler(w http.ResponseWriter, r *http.Request) {
	r.URL = c.BuildUpstreamURL(r)
	ObjectProxyCacheRequest(w, r)
}

func (c *TestClient) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	DoProxy(w, r, true)
}

func (c *TestClient) SetUpstreamLogging(bool) {
}

func testResultHeaderPartMatch(header http.Header, kvp map[string]string) error {
	if len(kvp) == 0 {
		return nil
	}
	if len(header) == 0 {
		return fmt.Errorf("missing response headers%s", "")
	}

	if h, ok := header["X-Trickster-Result"]; ok {
		res := strings.Join(h, "; ")
		for k, v := range kvp {
			if !strings.Contains(res, fmt.Sprintf("; %s=%s", k, v)) &&
				strings.Index(res, fmt.Sprintf("%s=%s", k, v)) != 0 {
				return fmt.Errorf("invalid status, expected %s=%s in %s", k, v, h)
			}
		}
	} else {
		return fmt.Errorf("missing X-Trickster-Result header%s", "")
	}

	return nil
}

func testStatusCodeMatch(have, expected int) error {
	if have != expected {
		return fmt.Errorf("expected http status %d got %d", expected, have)
	}
	return nil
}

func testStringMatch(have, expected string) error {
	if have != expected {
		return fmt.Errorf("expected string `%s` got `%s`", expected, have)
	}
	return nil
}
