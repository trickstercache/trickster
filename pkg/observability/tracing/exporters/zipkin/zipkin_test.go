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

// Package zipkin provides a Zipkin Tracer
package zipkin

import (
	"testing"

	errs "github.com/trickstercache/trickster/v2/pkg/observability/tracing/errors"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
)

func TestNew(t *testing.T) {

	_, err := New(nil)
	if err != errs.ErrNoTracerOptions {
		t.Error("expected error for no tracer options")
	}

	opt := options.New()
	opt.Tags = map[string]string{"test": "test"}
	opt.CollectorURL = "http://1.2.3.4:8000"

	_, err = New(opt)
	if err != nil {
		t.Error(err)
	}

	opt.SampleRate = 1
	_, err = New(opt)
	if err != nil {
		t.Error(err)
	}

	opt.SampleRate = 0.5
	_, err = New(opt)
	if err != nil {
		t.Error(err)
	}

	opt.CollectorURL = "1.2.3.4:5"
	_, err = New(opt)
	if err == nil {
		t.Error("expected error for invalid collector URL")
	}

}
