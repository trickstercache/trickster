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
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/timeseries"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

// ProxyRequest proxies an inbound request to its corresponding upstream origin with no caching features
func ProxyRequest(r *model.Request, w http.ResponseWriter) {
	body, resp, elapsed := Fetch(r)
	recordProxyResults(r, strconv.Itoa(resp.StatusCode), r.URL.Path, elapsed, resp.Header)
	Respond(w, resp.StatusCode, resp.Header, body)
}

// Fetch makes an HTTP request to the provided Origin URL
func Fetch(r *model.Request) ([]byte, *http.Response, time.Duration) {

	if r != nil {
		headers.AddProxyHeaders(r.ClientRequest.RemoteAddr, r.Headers)
	}

	headers.RemoveClientHeaders(r.Headers)

	start := time.Now()
	resp, err := r.HTTPClient.Do(&http.Request{Method: r.ClientRequest.Method, URL: r.URL, Header: r.Headers})
	if err != nil {
		log.Error("error downloading url", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
		// if there is an err and the response is nil, the server could not be reached; make a 502 for the downstream response
		if resp == nil {
			resp = &http.Response{StatusCode: http.StatusBadGateway, Request: r.ClientRequest, Header: make(http.Header)}
		}
		return []byte{}, resp, -1
	}

	resp.Header.Del(headers.NameContentLength)

	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Error("error reading body from http response", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
		return []byte{}, resp, 0
	}

	elapsed := time.Since(start) // includes any time required to decompress the document for deserialization

	if config.Logging.LogLevel == "debug" || config.Logging.LogLevel == "trace" {
		go logUpstreamRequest(r.OriginName, r.OriginType, r.HandlerName, r.ClientRequest.Method, r.URL.String(), r.ClientRequest.UserAgent(), resp.StatusCode, len(body), elapsed.Seconds())
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

func recordProxyResults(r *model.Request, httpStatus, path string, elapsed time.Duration, header http.Header) {
	recordResults(r, "HTTPProxy", "", httpStatus, path, "", elapsed, nil, header)
}

func recordResults(r *model.Request, engine, cacheStatus, httpStatus, path, ffStatus string, elapsed time.Duration, extents timeseries.ExtentList, header http.Header) {
	metrics.ProxyRequestStatus.WithLabelValues(r.OriginName, r.OriginType, r.ClientRequest.Method, cacheStatus, httpStatus, path).Inc()
	if elapsed > 0 {
		metrics.ProxyRequestDuration.WithLabelValues(r.OriginName, r.OriginType, r.ClientRequest.Method, cacheStatus, httpStatus, path).Observe(elapsed.Seconds())
	}
	headers.SetResultsHeader(header, engine, cacheStatus, ffStatus, extents)
}
