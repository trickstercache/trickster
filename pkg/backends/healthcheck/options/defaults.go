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

package options

import "net/http"

const (
	// DefaultHealthCheckPath is the default value (noop) for Backends' Health Check Path
	DefaultHealthCheckPath = "/"
	// DefaultHealthCheckQuery is the default value (noop) for Backends' Health Check Query Parameters
	DefaultHealthCheckQuery = ""
	// DefaultHealthCheckVerb is the default value (noop) for Backends' Health Check Verb
	DefaultHealthCheckVerb = http.MethodGet
	// DefaultHealthCheckTimeoutMS is the default duration for health check probes to wait before timing out
	DefaultHealthCheckTimeoutMS = 3000
	// DefaultHealthCheckRecoveryThreshold defines the default number of successful health checks
	// following failure to indicate true recovery
	DefaultHealthCheckRecoveryThreshold = 3
	// DefaultHealthCheckFailureThreshold defines the default number of failed health checks
	// following recovery or initial healthy to indicate true recovery
	DefaultHealthCheckFailureThreshold = 3
)
