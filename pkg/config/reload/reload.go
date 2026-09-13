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

// Package reload helps with reloading the running Trickster configuration
package reload

type Reloader func(string) (bool, error)

const (
	ConfigNotReloadedText = "configuration NOT reloaded"
	ConfigReloadedText    = "configuration reloaded"
)

const (
	// SourceSIGHUP identifies a reload requested by an operator via SIGHUP.
	SourceSIGHUP = "sighup"
	// SourceHTTP identifies a reload requested via the mgmt reload handler.
	SourceHTTP = "handler"
	// SourceAutoReload identifies a reload started by the auto-reload poller.
	SourceAutoReload = "auto-reload"
	// SourceKubernetes identifies a reload the Kubernetes controller started
	// because its translation of the watched objects changed.
	SourceKubernetes = "kubernetes"
)

// IsUserRequested reports whether source is an operator-initiated reload, which
// is subject to the reload rate limit; automatic and in-process reloads are not.
func IsUserRequested(source string) bool {
	return source == SourceSIGHUP || source == SourceHTTP
}
