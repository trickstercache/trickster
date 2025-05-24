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
	"errors"
	"fmt"
)

var ErrInvalidTimeSeriesMergeProvider = errors.New("invalid time series merge provider")
var ErrUnsupportedMechanism = errors.New("unsupported mechanism")
var ErrInvalidOptionsMetadata = errors.New("invalid options metadata")

// InvalidALBOptionsError is an error type for invalid ALB Options
type InvalidALBOptionsError struct {
	error
}

// NewErrInvalidALBOptions returns an invalid ALB Options error
func NewErrInvalidALBOptions(backendName string) error {
	return &InvalidALBOptionsError{
		error: fmt.Errorf("invalid alb options for backend [%s]",
			backendName),
	}
}

// NewErrInvalidPoolMemberName returns a new invalid ALB Options error
func NewErrInvalidPoolMemberName(albName, poolMemberName string) error {
	return &InvalidALBOptionsError{
		error: fmt.Errorf("invalid pool member name [%s] provided for alb [%s]",
			poolMemberName, albName),
	}
}

// NewErrInvalidBackendName returns a new invalid ALB Options error
func NewErrInvalidBackendName(albName, poolMemberName string) error {
	return &InvalidALBOptionsError{
		error: fmt.Errorf("invalid backend name [%s] provided in alb [%s]",
			poolMemberName, albName),
	}
}

// NewErrInvalidUserRouterCreds returns a new invalid User Router error
func NewErrInvalidUserRouterCreds(albName string) error {
	return &InvalidALBOptionsError{
		error: fmt.Errorf("alb [%s] an authenticator_name is required to use to_credential",
			albName),
	}
}
