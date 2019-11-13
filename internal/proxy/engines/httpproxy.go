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
	"io"
	"io/ioutil"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
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

// Used for Progressive Collapsed Forwarding
var Reqs sync.Map

const HTTPBlockSize = 32 * 1024

// ProxyRequest proxies an inbound request to its corresponding upstream origin with no caching features
func ProxyRequest(r *model.Request, w http.ResponseWriter) *http.Response {
	pc := context.PathConfig(r.ClientRequest.Context())
	oc := context.OriginConfig(r.ClientRequest.Context())

	start := time.Now()
	var elapsed time.Duration
	var cacheStatusCode tc.LookupStatus
	var resp *http.Response
	var reader io.Reader
	if pc != nil && !pc.ProgressiveCollapsedForwarding {
		reader, resp, _ = PrepareFetchReader(r)
		cacheStatusCode = setStatusHeader(resp.StatusCode, resp.Header)
		writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
		if writer != nil && reader != nil {
			io.Copy(writer, reader)
		}
	} else {
		key := oc.Host + "." + DeriveCacheKey(r, pc, "")
		result, ok := Reqs.Load(key)
		if !ok {
			cl := 0
			reader, resp, cl = PrepareFetchReader(r)
			cacheStatusCode = setStatusHeader(resp.StatusCode, resp.Header)
			writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
			// Check if we know the content length and if it is less than our max object size.
			if cl != 0 && cl < oc.MaxObjectSizeBytes {
				pcf := NewPCF(resp, cl)
				Reqs.Store(key, pcf)
				// Blocks until server completes
				go func() {
					io.Copy(pcf, reader)
					pcf.Close()
					Reqs.Delete(key)
				}()
				pcf.AddClient(writer)
			}
		} else {
			pcf, _ := result.(ProgressiveCollapseForwarder)
			resp = pcf.GetResp()
			writer := PrepareResponseWriter(w, resp.StatusCode, resp.Header)
			pcf.AddClient(writer)
		}
	}
	elapsed = time.Since(start)
	recordResults(r, "HTTPProxy", cacheStatusCode, resp.StatusCode, r.URL.Path, "", elapsed.Seconds(), nil, resp.Header)
	return resp
}

// PrepareResponseWriter prepares a response and returns an io.Writer for the data to be written to.
// Used in Respond.
func PrepareResponseWriter(w http.ResponseWriter, code int, header http.Header) io.Writer {
	h := w.Header()
	for k, v := range header {
		h.Set(k, strings.Join(v, ","))
	}
	headers.AddResponseHeaders(h)
	w.WriteHeader(code)
	return w
}

// PrepareFetchReader prepares an http response and returns io.ReadCloser to
// provide the response data, the response object and the content length.
// Used in Fetch.
func PrepareFetchReader(r *model.Request) (io.ReadCloser, *http.Response, int) {
	pc := context.PathConfig(r.ClientRequest.Context())
	oc := context.OriginConfig(r.ClientRequest.Context())

	var rc io.ReadCloser

	if r != nil && r.Headers != nil {
		headers.AddProxyHeaders(r.ClientRequest.RemoteAddr, r.Headers)
	}

	headers.RemoveClientHeaders(r.Headers)

	if pc != nil {
		headers.UpdateHeaders(r.Headers, pc.RequestHeaders)
	}

	req := &http.Request{Method: r.ClientRequest.Method}
	var err error
	req, err = http.NewRequest(r.ClientRequest.Method, r.URL.String(), r.ClientRequest.Body)
	if err != nil {
		return nil, nil, 0
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
		return nil, resp, 0
	}

	originalLen, _ := strconv.Atoi(resp.Header.Get("Content-Length"))
	rc = resp.Body

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

	if hasCustomResponseBody {
		rc = ioutil.NopCloser(bytes.NewBuffer(pc.ResponseBodyBytes))
	}

	return rc, resp, originalLen
}

// Fetch makes an HTTP request to the provided Origin URL
func Fetch(r *model.Request) ([]byte, *http.Response, time.Duration) {
	oc := context.OriginConfig(r.ClientRequest.Context())

	start := time.Now()
	reader, resp, _ := PrepareFetchReader(r)

	body, err := ioutil.ReadAll(reader)
	resp.Body.Close()
	if err != nil {
		log.Error("error reading body from http response", log.Pairs{"url": r.URL.String(), "detail": err.Error()})
		return []byte{}, resp, 0
	}

	elapsed := time.Since(start) // includes any time required to decompress the document for deserialization

	if config.Logging.LogLevel == "debug" || config.Logging.LogLevel == "trace" {
		go logUpstreamRequest(oc.Name, oc.OriginType, r.HandlerName, r.ClientRequest.Method, r.URL.String(), r.ClientRequest.UserAgent(), resp.StatusCode, len(body), elapsed.Seconds())
	}

	return body, resp, elapsed
}

// Respond sends an HTTP Response down to the requesting client
func Respond(w http.ResponseWriter, code int, header http.Header, body []byte) {
	PrepareResponseWriter(w, code, header)
	w.Write(body)
}

func setStatusHeader(httpStatus int, header http.Header) tc.LookupStatus {
	status := tc.LookupStatusProxyOnly
	if httpStatus >= http.StatusBadRequest {
		status = tc.LookupStatusProxyError
	}
	headers.SetResultsHeader(header, "HTTPProxy", status.String(), "", nil)
	return status
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
