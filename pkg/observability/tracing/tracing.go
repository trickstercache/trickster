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

// Package tracing provides distributed tracing services to Trickster
package tracing

import (
	"context"
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ShutdownFunc defines a function used to Flush a Tracer
type ShutdownFunc func(context.Context) error

// Tracer is a Tracer object used by Trickster
type Tracer struct {
	trace.Tracer
	Name         string
	ShutdownFunc ShutdownFunc
	Options      *options.Options
}

// Tracers is a map of *Tracer objects
type Tracers map[string]*Tracer

// Tags represents a collection of Tags
type Tags map[string]string

// HTTPToCode translates an HTTP status code into a GRPC code
func HTTPToCode(status int) codes.Code {
	switch {
	case status < http.StatusBadRequest:
		return codes.Ok
	default:
		return codes.Error
	}
}

// Merge merges t2, when not nil, into t
func (t Tags) Merge(t2 Tags) {
	if t2 == nil {
		return
	}
	for k, v := range t2 {
		t[k] = v
	}
}

// MergeAttr merges the provided attributes into the Tags map
func (t Tags) MergeAttr(attr []attribute.KeyValue) {
	if len(attr) == 0 {
		return
	}
	for _, v := range attr {
		t[string(v.Key)] = v.Value.AsString()
	}
}

// ToAttr returns the Tags map as an Attributes List
func (t Tags) ToAttr() []attribute.KeyValue {
	attr := make([]attribute.KeyValue, len(t))
	i := 0
	for k, v := range t {
		attr[i] = attribute.String(k, v)
		i++
	}
	return attr
}
