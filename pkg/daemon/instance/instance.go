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

package instance

import (
	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/dynamic"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/discovery"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
	"github.com/trickstercache/trickster/v2/pkg/proxy/tls/monitor"
)

type ServerInstance struct {
	Config           *config.Config
	Caches           cache.Lookup
	HealthChecker    healthcheck.HealthChecker
	Backends         backends.Backends
	Listeners        *listener.Group
	OnConfigReloaded func(*config.Config)
	// Discoverers holds the running autodiscovery provider instances,
	// keyed by discoverer name; rebuilt on each config (re)load
	Discoverers map[string]discovery.Discoverer
	// PoolManagers holds the dynamic pool manager for each
	// discovery-backed ALB, keyed by ALB backend name
	PoolManagers map[string]*dynamic.Manager
	CertMonitor  *monitor.Monitor
}
