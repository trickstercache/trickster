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

package druid

import "errors"

var (
	errInvalidRequest   = errors.New("invalid Druid request")
	errInvalidJSON      = errors.New("invalid Druid JSON query")
	errObjectCache      = errors.New("druid query requires object cache")
	errInvalidRewrite   = errors.New("invalid Druid extent rewrite input")
	errMissingQueryPlan = errors.New("druid query plan is missing")
	errInvalidQueryStep = errors.New("druid query step is invalid")
	errRenderQuery      = errors.New("druid query rendering failed")
)

type classifiedError struct {
	base   error
	reason analysisReason
}

func (e *classifiedError) Error() string {
	return e.base.Error() + ": " + string(e.reason)
}

func (e *classifiedError) Unwrap() error { return e.base }

func newClassifiedError(base error, reason analysisReason) error {
	return &classifiedError{base: base, reason: reason}
}
