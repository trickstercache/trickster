/*
 * Copyright 2026 The Trickster Authors
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

package handler

import (
	"bufio"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ratelimit/options"
)

// policyWriter adds this limiter's policy fields when the response is committed. IETF fields are
// added, so an outer limiter keeps the inner ones. Legacy fields are set only when still absent.
type policyWriter struct {
	http.ResponseWriter
	a     *attachment
	d     ratelimit.Decision
	wrote bool
}

func (w *policyWriter) WriteHeader(code int) {
	w.stamp()
	w.ResponseWriter.WriteHeader(code)
}

func (w *policyWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *policyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *policyWriter) Flush() {
	w.stamp()
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *policyWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *policyWriter) stamp() {
	if w.wrote {
		return
	}
	w.wrote = true
	writePolicy(w.Header(), w.a, w.d)
}

func writePolicy(h http.Header, a *attachment, d ratelimit.Decision) {
	if a == nil || h == nil || a.policy == options.PolicyNone {
		return
	}
	sec := ratelimit.RetryAfterSeconds(d.Reset)
	switch a.policy {
	case options.PolicyIETF:
		name := sfString(a.name)
		wsec := ratelimit.RetryAfterSeconds(time.Duration(a.window))
		h.Add(headers.NameRateLimitPolicy, name+";q="+strconv.FormatUint(uint64(a.limit), 10)+";w="+strconv.Itoa(wsec))
		h.Add(headers.NameRateLimit, name+";r="+strconv.FormatUint(uint64(d.Remaining), 10)+";t="+strconv.Itoa(sec))
	case options.PolicyLegacy:
		setIfAbsent(h, headers.NameXRateLimitLimit, strconv.FormatUint(uint64(a.limit), 10))
		setIfAbsent(h, headers.NameXRateLimitRemaining, strconv.FormatUint(uint64(d.Remaining), 10))
		setIfAbsent(h, headers.NameXRateLimitReset, strconv.FormatInt(time.Now().Unix()+int64(sec), 10))
	}
}

func setIfAbsent(h http.Header, key, value string) {
	if h.Get(key) != "" {
		return
	}
	h.Set(key, value)
}

func sfString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '"' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}
