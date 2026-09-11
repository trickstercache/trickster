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

// Package cors applies backend and path CORS policies to downstream responses.
package cors

import (
	"net/http"
	"strings"

	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

const corsHeaderPrefix = "access-control-"

// Apply applies a CORS policy to a response header collection.
func Apply(h http.Header, o *corso.Options) {
	if h == nil {
		return
	}
	if o == nil {
		o = corso.Legacy()
	}
	if o.IsLegacy() {
		deleteHeader(h, headers.NameAllowOrigin)
		headers.AddResponseHeaders(h)
		return
	}
	mode := corso.Mode(strings.ToLower(string(o.Mode)))
	if mode == "" {
		mode = corso.ModeReplace
	}
	if mode == corso.ModePreserve {
		return
	}
	if mode == corso.ModeReplace || mode == corso.ModeDisable {
		for name := range h {
			if strings.HasPrefix(strings.ToLower(name), corsHeaderPrefix) {
				delete(h, name)
			}
		}
	}
	if mode == corso.ModeDisable {
		return
	}
	updateHeaders(h, o.Headers)
}

func updateHeaders(h http.Header, updates map[string]string) {
	for configuredName, value := range updates {
		if configuredName == "" {
			continue
		}
		operation := byte(0)
		name := configuredName
		if name[0] == '+' || name[0] == '-' {
			operation = name[0]
			name = name[1:]
		}
		if name == "" {
			continue
		}
		switch operation {
		case '-':
			deleteHeader(h, name)
		case '+':
			values := takeHeader(h, name)
			for _, existing := range values {
				h.Add(name, existing)
			}
			h.Add(name, value)
		default:
			deleteHeader(h, name)
			h.Set(name, value)
		}
	}
}

func takeHeader(h http.Header, name string) []string {
	var values []string
	for existing, existingValues := range h {
		if strings.EqualFold(existing, name) {
			values = append(values, existingValues...)
			delete(h, existing)
		}
	}
	return values
}

func deleteHeader(h http.Header, name string) {
	for existing := range h {
		if strings.EqualFold(existing, name) {
			delete(h, existing)
		}
	}
}

// ResponseWriter applies a CORS policy before the final response headers are written.
type ResponseWriter interface {
	http.ResponseWriter
	Finalize()
}

// Wrap returns a response writer that applies the policy before response headers are written.
func Wrap(w http.ResponseWriter, o *corso.Options) ResponseWriter {
	return &responseWriter{ResponseWriter: w, options: o}
}

type responseWriter struct {
	http.ResponseWriter
	options     *corso.Options
	wroteHeader bool
	applied     bool
}

func (w *responseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.wroteHeader = true
	w.apply()
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	// The selected handler owns content type and contextual escaping; this wrapper
	// must forward reverse-proxied response bodies byte-for-byte.
	return w.ResponseWriter.Write(p)
}

// Finalize applies the policy when a handler returns without explicitly writing a response.
func (w *responseWriter) Finalize() {
	if !w.wroteHeader {
		w.apply()
	}
}

func (w *responseWriter) apply() {
	if w.applied {
		return
	}
	w.applied = true
	Apply(w.Header(), w.options)
}

func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
