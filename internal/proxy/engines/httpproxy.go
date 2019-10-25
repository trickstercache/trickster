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
	"bytes"
	"io/ioutil"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	tc "github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

// ProxyRequest proxies an inbound request to its corresponding upstream origin with no caching features
func ProxyRequest(r *model.Request, w http.ResponseWriter) {
	body, resp, elapsed := Fetch(r)
	recordProxyResults(r, resp.StatusCode, r.URL.Path, elapsed.Seconds(), resp.Header)
	Respond(w, resp.StatusCode, resp.Header, body)
}

// Fetch makes an HTTP request to the provided Origin URL
func Fetch(r *model.Request) ([]byte, *http.Response, time.Duration) {

	pc := context.PathConfig(r.ClientRequest.Context())
	oc := context.OriginConfig(r.ClientRequest.Context())

	if r != nil && r.Headers != nil {
		headers.AddProxyHeaders(r.ClientRequest.RemoteAddr, r.Headers)
	}

	headers.RemoveClientHeaders(r.Headers)

	if pc != nil {
		headers.UpdateHeaders(r.Headers, pc.RequestHeaders)
	}

	start := time.Now()
	req := &http.Request{Method: r.ClientRequest.Method}
	b, err := ioutil.ReadAll(r.ClientRequest.Body)
	if err == nil && len(b) > 0 {
		req, err = http.NewRequest(r.ClientRequest.Method, r.URL.String(),
			bytes.NewBuffer(b))
		if err != nil {
			return []byte{}, nil, -1
		}
	}

	req.Header = r.Headers
	req.URL = r.URL
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		log.Error("error downloading url", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
		// if there is an err and the response is nil, the server could not be reached; make a 502 for the downstream response
		if resp == nil {
			resp = &http.Response{StatusCode: http.StatusBadGateway, Request: r.ClientRequest, Header: make(http.Header)}
		}
		if pc != nil {
			headers.UpdateHeaders(resp.Header, pc.ResponseHeaders)
		}
		return []byte{}, resp, -1
	}

	// warn if the clock between trickster and the origin is off by more than 1 minute
	if date := resp.Header.Get(headers.NameDate); date != "" {
		d, err := http.ParseTime(date)
		if err == nil {
			if offset := time.Now().Sub(d); time.Duration(math.Abs(float64(offset))) > time.Minute {
				log.WarnOnce("clockoffset."+oc.Name,
					"clock offset between trickster host and origin is high and may cause data anomalies",
					log.Pairs{
						"originName":    oc.Name,
						"tricksterTime": strconv.FormatInt(d.Add(offset).Unix(), 10),
						"originTime":    strconv.FormatInt(d.Unix(), 10),
						"offset":        strconv.FormatInt(int64(offset.Seconds()), 10) + "s",
					})
			}
		}
	}

	hasCustomResponseBody := false
	resp.Header.Del(headers.NameContentLength)

	if pc != nil {
		headers.UpdateHeaders(resp.Header, pc.ResponseHeaders)
		hasCustomResponseBody = pc.HasCustomResponseBody
	}

	var body []byte

	if hasCustomResponseBody {
		body = pc.ResponseBodyBytes
	} else {
		body, err = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			log.Error("error reading body from http response", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
			return []byte{}, resp, 0
		}
	}

	elapsed := time.Since(start) // includes any time required to decompress the document for deserialization

	if config.Logging.LogLevel == "debug" || config.Logging.LogLevel == "trace" {
		go logUpstreamRequest(oc.Name, oc.OriginType, r.HandlerName, r.ClientRequest.Method, r.URL.String(), r.ClientRequest.UserAgent(), resp.StatusCode, len(body), elapsed.Seconds())
	}

	return body, resp, elapsed
}

// Respond sends an HTTP Response down to the requesting client
func Respond(w http.ResponseWriter, code int, header http.Header, body []byte) {
	h := w.Header()
	for k, v := range header {
		h.Set(k, strings.Join(v, ","))
	}
	headers.AddResponseHeaders(h)
	w.WriteHeader(code)
	w.Write(body)
}

func recordProxyResults(r *model.Request, httpStatus int, path string, elapsed float64, header http.Header) {
	status := tc.LookupStatusProxyOnly
	if httpStatus >= http.StatusBadRequest {
		status = tc.LookupStatusProxyError
	}
	recordResults(r, "HTTPProxy", status, httpStatus, path, "", elapsed, nil, header)
}

func recordResults(r *model.Request, engine string, cacheStatus tc.LookupStatus, statusCode int, path, ffStatus string, elapsed float64, extents timeseries.ExtentList, header http.Header) {

	oc := context.OriginConfig(r.ClientRequest.Context())
	pc := context.PathConfig(r.ClientRequest.Context())
	status := cacheStatus.String()

	if pc != nil && !pc.NoMetrics {
		httpStatus := strconv.Itoa(statusCode)
		metrics.ProxyRequestStatus.WithLabelValues(oc.Name, oc.OriginType, r.HTTPMethod, status, httpStatus, path).Inc()
		if elapsed > 0 {
			metrics.ProxyRequestDuration.WithLabelValues(oc.Name, oc.OriginType, r.HTTPMethod, status, httpStatus, path).Observe(elapsed)
		}
	}
	headers.SetResultsHeader(header, engine, status, ffStatus, extents)
}
