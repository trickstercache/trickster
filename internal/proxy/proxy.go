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
	"strings"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/compress/gzip"
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
	metrics.ProxyRequestStatus.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, "none", r.URL.Path).Inc()
	body, resp, dur := Fetch(r)
	metrics.ProxyRequestDuration.WithLabelValues(r.OriginName, r.OriginType, r.HTTPMethod, "none", r.URL.Path).Observe(float64(dur))
	Respond(w, resp.StatusCode, resp.Header, body)
}

// Fetch makes an HTTP request to the provided Origin URL
func Fetch(r *Request) ([]byte, *http.Response, int) {

	if r != nil {
		addProxyHeaders(r.ClientRequest.RemoteAddr, r.Headers)
	} else {
		addClientHeaders(r.Headers)
	}

	u := r.URL.String()
	start := time.Now()
	client := &http.Client{}
	resp, err := client.Do(&http.Request{Method: r.ClientRequest.Method, URL: r.URL, Header: r.ClientRequest.Header})
	if err != nil {
		log.Error("error downloading url", log.Pairs{"url": u, "detail": err.Error()})
		return []byte{}, resp, -1
	}

	resp.Header.Del(hnContentLength)

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Error("error reading body from http response", log.Pairs{"url": u, "detail": err.Error()})
		return []byte{}, resp, 0
	}

	// Decompress the content here since the gorilla compress will handle it.
	// Not optimal, we should pass the compressed document through to the client
	// and only use the gorilla compress handler if it is not already compressed
	if h, ok := resp.Header[hnContentEncoding]; ok {
		if h[0] == "gzip" {
			body, _ = gzip.Inflate(body)
			resp.Header.Del(hnContentEncoding)
		}
	}

	resp.Body.Close()
	duration := int(time.Since(start).Nanoseconds() / 1000000)
	go logUpstreamRequest(r.OriginName, r.OriginType, r.HandlerName, r.HTTPMethod, u, r.ClientRequest.UserAgent(), resp.StatusCode, len(body), duration)
	return body, resp, duration
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
