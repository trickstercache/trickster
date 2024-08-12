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

import "errors"

// ErrNilWriter is an error for a nil writer when a non-nil writer was expected
var ErrNilWriter = errors.New("nil writer")

// ErrInvalidOptions is an error for when a configuration is invalid
var ErrInvalidOptions = errors.New("invalid options")

// ErrMissingPathconfig is an error for when a configuration is missing a path value
var ErrMissingPathConfig = errors.New("missing path config")

// ErrInvalidPath is an error for when a configuration's path is invalid
var ErrInvalidPath = errors.New("invalid path value in config")

// ErrInvalidMethod is an error for when a configuration's method is invalid
var ErrInvalidMethod = errors.New("invalid method value in config")
