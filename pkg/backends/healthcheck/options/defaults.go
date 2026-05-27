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

import (
	"time"
)

const (
	// DefaultHealthCheckTimeout is the default duration for health check probes to wait before timing out
	DefaultHealthCheckTimeout = 3 * time.Second

	// DefaultAutoProbeInterval is applied when StartHealthChecks auto-installs
	// a provider's default health-check config because the operator did not
	// configure one. The auto-applied probe must fire on a tick or the
	// downstream pool filter will never see a status transition.
	DefaultAutoProbeInterval = 5 * time.Second
)
