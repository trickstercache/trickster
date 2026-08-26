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

package accesslog

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/format"
	authtypes "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// Middleware wraps next with a recorder that writes an access log line to
// the Logger after each request completes
func Middleware(l *Logger, pathConfig string, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rsc := request.GetResources(r)
		if rsc == nil {
			rsc = &request.Resources{}
			r = request.SetResources(r, rsc)
		}
		f := &format.Fields{
			StartTime:  time.Now(),
			Method:     r.Method,
			RequestURI: r.RequestURI,
			Path:       r.URL.Path,
			Query:      r.URL.RawQuery,
			Proto:      r.Proto,
			Host:       r.Host,
			PathConfig: pathConfig,
			ReqHeader:  r.Header,
		}
		f.ClientIP = clientIP(r.RemoteAddr)
		if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			f.LocalIP, f.LocalPort = splitAddr(la.String())
		}
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		f.Duration = time.Since(f.StartTime)
		f.Status = rec.status
		f.BytesWritten = rec.bytes
		if rsc.AuthResult != nil && rsc.AuthResult.Status == authtypes.AuthSuccess {
			f.User = rsc.AuthResult.Username
		}
		f.RespHeader = w.Header()
		f.Engine, f.CacheStatus = parseResultHeader(
			w.Header().Get(headers.NameTricksterResult))
		l.Log(f)
	})
}

type recorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *recorder) WriteHeader(statusCode int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(statusCode)
	r.status = statusCode
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (r *recorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func clientIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func splitAddr(addr string) (string, string) {
	if host, port, err := net.SplitHostPort(addr); err == nil {
		return host, port
	}
	return addr, ""
}

// parseResultHeader extracts the engine and cache status from an
// X-Trickster-Result header value (e.g., "engine=DeltaProxyCache; status=hit")
func parseResultHeader(value string) (engine, status string) {
	for value != "" {
		var part string
		part, value, _ = strings.Cut(value, ";")
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "engine":
			engine = v
		case "status":
			status = v
		}
	}
	return
}
