/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

// Package tracing provides distributed tracing services to Trickster
package tracing

import (
	"net/http"

	"github.com/tricksterproxy/trickster/pkg/tracing/options"
	"google.golang.org/grpc/codes"

	"go.opentelemetry.io/otel/api/trace"
)

// FlusherFunc defines a function used to Flush a Tracer
type FlusherFunc func()

// Tracer is a Tracer object used by Trickster
type Tracer struct {
	trace.Tracer
	Name    string
	Flusher FlusherFunc
	Options *options.Options
}

// Tracers is a map of *Tracer objects
type Tracers map[string]*Tracer

// HTTPToCode translates an HTTP status code into a GRPC code
func HTTPToCode(status int) codes.Code {
	switch {
	case status < http.StatusBadRequest:
		return codes.OK
	case status == http.StatusNotFound:
		return codes.NotFound
	case status < http.StatusInternalServerError:
		// All other 4xx
		return codes.InvalidArgument
	case status == http.StatusServiceUnavailable:
		return codes.Unavailable
	default: // all remaining possiblitiies are values >= 500
		return codes.Internal
	}
}
