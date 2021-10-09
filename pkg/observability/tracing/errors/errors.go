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

// Package errors provides tracing errors
package errors

import "errors"

// ErrNoTracerOptions is an error for when GetTracer is called with nil *Options
var ErrNoTracerOptions = errors.New("no tracer options provided")

// ErrInvalidEndpointURL is an error for when the endpoint URL is invalid for the provider
var ErrInvalidEndpointURL = errors.New("invalid endpoint url")
