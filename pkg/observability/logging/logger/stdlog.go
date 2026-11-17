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

package logger

import (
	"bytes"
	stdlog "log"
	"maps"

	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

const (
	stdlogEvent          = "stdlib log"
	httpServerErrorEvent = "http server error"
	tlsHandshakeEvent    = "tls handshake failed"
	httpServerSource     = "net/http"
)

// net/http logs this for every failed handshake, which internet scanners cause constantly
var tlsHandshakePrefix = []byte("http: TLS handshake error")

// stdlogWriter forwards stdlib log lines to the package-level logger, resolved per write so a
// logger replaced on reload is used
type stdlogWriter struct {
	event string
	pairs logging.Pairs
}

func (w *stdlogWriter) Write(p []byte) (int, error) {
	n := len(p)
	p = bytes.TrimRight(p, "\n")
	if len(p) == 0 {
		return n, nil
	}
	if w.event == httpServerErrorEvent && bytes.HasPrefix(p, tlsHandshakePrefix) {
		if !DebugEnabled() {
			return n, nil
		}
		Debug(tlsHandshakeEvent, w.detail(p))
		return n, nil
	}
	Warn(w.event, w.detail(p))
	return n, nil
}

func (w *stdlogWriter) detail(p []byte) logging.Pairs {
	out := make(logging.Pairs, len(w.pairs)+1)
	maps.Copy(out, w.pairs)
	out[keys.Detail] = string(p)
	return out
}

// HTTPServerErrorLog returns an http.Server ErrorLog that writes to the package-level logger;
// TLS handshake failures are logged at DEBUG and everything else at WARN
func HTTPServerErrorLog(listenerName string) *stdlog.Logger {
	return stdlog.New(&stdlogWriter{
		event: httpServerErrorEvent,
		pairs: logging.Pairs{keys.ListenerName: listenerName, keys.Source: httpServerSource},
	}, "", 0)
}

// CaptureStdLog points the stdlib log package's default logger at the package-level logger,
// reclaiming it from any dependency that redirected it in an init function
func CaptureStdLog() {
	stdlog.SetFlags(0)
	stdlog.SetPrefix("")
	stdlog.SetOutput(&stdlogWriter{event: stdlogEvent})
}
