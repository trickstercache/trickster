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
	"strconv"
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

const (
	// Cache lookup results
	crKeyMiss    = "kmiss"
	crRangeMiss  = "rmiss"
	crHit        = "hit"
	crPartialHit = "phit"
	crPurge      = "purge"
)

// ProxyRequest ...
func ProxyRequest(r *Request, w http.ResponseWriter) {
	body, resp, elapsed := Fetch(r)
	metrics.ProxyRequestStatus.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, "none", strconv.Itoa(resp.StatusCode), r.URL.Path).Inc()
	metrics.ProxyRequestDuration.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, "none", strconv.Itoa(resp.StatusCode), r.URL.Path).Observe(elapsed.Seconds())
	Respond(w, resp.StatusCode, resp.Header, body)
}

// Fetch makes an HTTP request to the provided Origin URL
func Fetch(r *Request) ([]byte, *http.Response, time.Duration) {

	if r != nil {
		addProxyHeaders(r.ClientRequest.RemoteAddr, r.Headers)
	} else {
		addClientHeaders(r.Headers)
	}

	removeClientHeaders(r.Headers)

	start := time.Now()
	client := &http.Client{Timeout: r.Timeout}
	resp, err := client.Do(&http.Request{Method: r.ClientRequest.Method, URL: r.URL, Header: r.Headers})
	if err != nil {
		log.Error("error downloading url", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
		// if there is an err and the response is nil, the server could not be reached; make a 502 for the downstream response
		if resp == nil {
			resp = &http.Response{StatusCode: http.StatusBadGateway, Request: r.ClientRequest}
		}
		return []byte{}, resp, -1
	}

	resp.Header.Del(hnContentLength)

	body, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		log.Error("error reading body from http response", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
		return []byte{}, resp, 0
	}

	latency := time.Since(start) // includes any time required to decompress the document for deserialization

	if config.Logging.LogLevel == "debug" || config.Logging.LogLevel == "trace" {
		go logUpstreamRequest(r.OriginName, r.OriginType, r.HandlerName, r.HTTPMethod, r.URL.String(), r.ClientRequest.UserAgent(), resp.StatusCode, len(body), latency.Seconds())
	}

	return body, resp, latency
}

// Respond ...
func Respond(w http.ResponseWriter, code int, headers http.Header, body []byte) {
	for k, v := range headers {
		w.Header().Set(k, strings.Join(v, ","))
	}
	w.Header().Set(hnXAccelerator, config.ApplicationName+" "+config.ApplicationVersion)
	w.WriteHeader(code)
	w.Write(body)
}
