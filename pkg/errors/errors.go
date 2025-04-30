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

package errors

import (
	"encoding/json"
	"errors"
)

// ErrNilWriter is an error for a nil writer when a non-nil writer was expected
var ErrNilWriter = errors.New("nil writer")

// ErrInvalidOptions is an error for when a configuration is invalid
var ErrInvalidOptions = errors.New("invalid options")

// ErrServerAlreadyStarted is an error for when daemon.Start() is called
// more than once
var ErrServerAlreadyStarted = errors.New("server is already started")

// ErrMissingPathconfig is an error for when a configuration is missing a path value
var ErrMissingPathConfig = errors.New("missing path config")

// ErrInvalidPath is an error for when a configuration's path is invalid
var ErrInvalidPath = errors.New("invalid path value in config")

// ErrInvalidMethod is an error for when a configuration's method is invalid
var ErrInvalidMethod = errors.New("invalid method value in config")

// ErrNoValidBackends is an error for when not valid backends have been configured
var ErrNoValidBackends = errors.New("no valid backends configured")

type ErrorBody struct {
	Error string `json:"error"`
}

func NewErrorBody(err error) string {
	if err == nil {
		return "{}"
	}
	eb := &ErrorBody{
		Error: err.Error(),
	}
	b, _ := json.Marshal(eb)
	return string(b)
}
