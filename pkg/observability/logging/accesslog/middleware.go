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
	"context"
	"encoding/binary"
	"encoding/hex"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/format"
	authtypes "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	utilmiddleware "github.com/trickstercache/trickster/v2/pkg/util/middleware"
)

// UnmatchedName is the backend and provider name logged for requests that no
// backend route handled (router 404s, ping, health and management routes).
const UnmatchedName = "-"

type logStateKey struct{}

// logState lets a backend route tell the enclosing RouterMiddleware that it
// handled the request, whether or not it wrote its own log line.
type logState struct {
	handled bool
}

// Handled marks requests reaching next as handled by a backend route, so the
// enclosing RouterMiddleware never attributes them to an unmatched route.
// Used for routes whose backend has opted out of access logging.
func Handled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		markHandled(r)
		next.ServeHTTP(w, r)
	})
}

func markHandled(r *http.Request) {
	if st, ok := r.Context().Value(logStateKey{}).(*logState); ok {
		st.handled = true
	}
}

// Middleware wraps next with a recorder that writes an access log line to the Logger after
// each request completes; withResources shares request resources with the route, for the
// authenticated user and a result header the route withholds from the client
func Middleware(l *Logger, pathConfig string, withResources bool,
	next http.Handler,
) http.Handler {
	if l == nil {
		return next
	}
	withResources = withResources || l.NeedsResources()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rsc *request.Resources
		if withResources {
			rsc = request.GetResources(r)
		}
		if withResources && rsc == nil {
			rsc = &request.Resources{}
			r = request.SetResources(r, rsc)
		}
		f := newFields(l, r, w, pathConfig)
		rec := utilmiddleware.NewResponseObserver(w)
		next.ServeHTTP(rec, r)
		finishFields(f, rec, w)
		if rsc != nil && rsc.AuthResult != nil &&
			rsc.AuthResult.Status == authtypes.AuthSuccess {
			f.User = rsc.AuthResult.Username
		}
		if l.NeedsResultHeader() {
			result := w.Header().Get(headers.NameTricksterResult)
			if result == "" && rsc != nil {
				result = rsc.HiddenResult
			}
			f.Engine, f.CacheStatus = headers.ParseResultEngineStatus(result)
		}
		if rsc != nil && l.NeedsResources() {
			f.UpstreamAddr, f.UpstreamStatus, f.UpstreamDuration = rsc.Upstream()
			if rsc.SpanContext.IsValid() {
				f.TraceID = rsc.SpanContext.TraceID().String()
				f.SpanID = rsc.SpanContext.SpanID().String()
			}
		}
		l.Log(f)
		markHandled(r)
	})
}

// RouterMiddleware wraps a whole router so requests that no route-level
// Middleware logged (unmatched paths, built-in handlers) still produce a
// line, attributed to UnmatchedName.
func RouterMiddleware(l *Logger, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		st := &logState{}
		r = r.WithContext(context.WithValue(r.Context(), logStateKey{}, st))
		f := newFields(l, r, w, "")
		rec := utilmiddleware.NewResponseObserver(w)
		next.ServeHTTP(rec, r)
		if st.handled {
			return
		}
		finishFields(f, rec, w)
		l.Log(f)
	})
}

func newFields(l *Logger, r *http.Request, w http.ResponseWriter, pathConfig string) *format.Fields {
	// when the format logs a request ID, the received X-Request-ID (or one assigned here) is
	// set on the request, where the upstream copy carries it, and echoed on the response
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
	f.RemoteIP = clientIP(r.RemoteAddr)
	f.ClientIP = request.ClientIP(r)
	if la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		f.LocalIP, f.LocalPort = splitAddr(la.String())
	}
	if l.NeedsRequestID() {
		f.RequestID = r.Header.Get(headers.NameXRequestID)
		if f.RequestID == "" {
			f.RequestID = newRequestID()
			r.Header.Set(headers.NameXRequestID, f.RequestID)
		}
		w.Header().Set(headers.NameXRequestID, f.RequestID)
	}
	return f
}

func newRequestID() string {
	// a 128-bit identifier correlates log lines and is not a secret, so the fast generator will do
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], rand.Uint64()) //nolint:gosec // correlation id, not a secret
	binary.BigEndian.PutUint64(b[8:], rand.Uint64()) //nolint:gosec // correlation id, not a secret
	return hex.EncodeToString(b[:])
}

func finishFields(f *format.Fields, rec *utilmiddleware.ResponseObserver, w http.ResponseWriter) {
	f.Duration = time.Since(f.StartTime)
	f.Status = rec.StatusCode()
	f.BytesWritten = rec.BytesWritten()
	f.RespHeader = w.Header()
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
