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

// Package errors provides common Error functionality to the Trickster proxy
package errors

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnexpectedUpstreamResponse indicates the http.Response received from an upstream origin
// indicates the request did not succeed due to a request error or origin-side error
var ErrUnexpectedUpstreamResponse = errors.New("unexpected upstream response")

// ErrServerRequestNotCompleted indicates the remote origin could not service the request
var ErrServerRequestNotCompleted = errors.New("server request not completed")

// ErrReadIndexTooLarge is an error indicating the read index is too large
var ErrReadIndexTooLarge = errors.New("read index too large")

// ErrNilCacheDocument indicates a cache object reference is nil
var ErrNilCacheDocument = errors.New("nil cache document")

// ErrEmptyDocumentBody indicates a cached object did not contain an HTTP Document upon retrieval
var ErrEmptyDocumentBody = errors.New("empty document body")

// ErrStepParse indicates an error parsing the step interval of a time series request
var ErrStepParse = errors.New("unable to parse timeseries step from downstream request")

// ErrNotSelectStatement indicates an error that the time series request is not a read-only select query
var ErrNotSelectStatement = errors.New("not a select statement")

// ErrNotTimeRangeQuery indicates an error that the time series request does not contain a query
var ErrNotTimeRangeQuery = errors.New("not a time range query")

// ErrNoRanges indicates an error that the range request does not contain any usable ranges
var ErrNoRanges = errors.New("no usable ranges")

// ErrInvalidRuleOptions indicates an error that the provided rule options were invalid
var ErrInvalidRuleOptions = errors.New("invalid rule options")

// ErrNilListener indicates an error that the underlying net.Listener is nil
var ErrNilListener = errors.New("nil listener")

// ErrNoSuchListener indicates an error that the provided listener name is unknown
var ErrNoSuchListener = errors.New("no such listener")

// ErrDrainTimeout indicates an error that the connection drain took longer than the requested timeout
var ErrDrainTimeout = errors.New("timed out draining")

// ErrPCFContentLength indicates that a response's content length does not permit PCF
var ErrPCFContentLength = errors.New("content length does not permit PCF")

// ErrUnsupportedEncoding indicates that the client requested an encoding that is not supported by Trickster
var ErrUnsupportedEncoding = errors.New("unsupported ecoding format requested")

// MissingURLParam returns a Formatted Error
func MissingURLParam(param string) error {
	return fmt.Errorf("missing URL parameter: [%s]", param)
}

// TimeArrayEmpty returns a Formatted Error
func TimeArrayEmpty(param string) error {
	return fmt.Errorf("time array is nil or empty: [%s]", param)
}

// InvalidPath returns an error indicating the request path is not valid.
func InvalidPath(path string) error {
	return fmt.Errorf("invalid request path: %s", path)
}

// ParseDuration returns a Duration Parsing Error
func ParseDuration(input string) (time.Duration, error) {
	return time.Duration(0), fmt.Errorf("unable to parse duration: %s", input)
}

// ParseRequestBody returns an error indicating the request body could not
// parsed into a valid value.
func ParseRequestBody(err error) error {
	return fmt.Errorf("unable to parse request body: %v", err)
}

// MissingRequestParam returns an error indicating the request is missing a
// required parameter.
func MissingRequestParam(param string) error {
	return fmt.Errorf("missing request parameter: %s", param)
}

// CouldNotFindKey returns an error indicating the key could not be found in the document
func CouldNotFindKey(name string) error {
	return fmt.Errorf("could not find key: %s", name)
}
