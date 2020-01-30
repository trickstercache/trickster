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

package tracing

import (
	"errors"
	"testing"

	"go.opentelemetry.io/otel/api/trace"
)

func TestNewChildSpan(t *testing.T) {

	// test with nil tracer:
	_, span := NewChildSpan(nil, nil, "test")

	if _, ok := span.(trace.NoopSpan); !ok {
		t.Error(errors.New("expected NoopSpan"))
	}

	// test with nil context but non-nil tracer

	tr, flush, _, err := SetTracer(OpenTelemetryTracer, StdoutExporter, "", 1)
	if tr == nil {
		t.Error(errors.New("expected non-nil tracer"))
	}

	if err != nil {
		t.Error(err)
	}

	ctx, span := NewChildSpan(nil, tr, "test")
	if ctx == nil {
		t.Error(errors.New("expected non-nil context"))
	}

	if span == nil {
		t.Error(errors.New("expected non-nil span"))
	}

	flush()

}
