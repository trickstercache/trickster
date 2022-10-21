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

package config

const (
	// DefaultConfigHandlerPath is the default value for the Trickster Config Printout Handler path
	DefaultConfigHandlerPath = "/trickster/config"
	// DefaultPingHandlerPath is the default value for the Trickster Config Ping Handler path
	DefaultPingHandlerPath = "/trickster/ping"
	// DefaultHealthHandlerPath defines the default path for the Health Handler
	DefaultHealthHandlerPath = "/trickster/health"
	// DefaultPurgeKeyHandlerPath defines the default path for the Cache Purge (by Key) Handler
	DefaultPurgeKeyHandlerPath = "/trickster/purge/key/{backend}/{key}"
	// DefaultPurgePathHandlerPath defines the default path for the Cache Purge (by Path) Handler
	// Requires ?backend={backend}&path={path}
	DefaultPurgePathHandlerPath = "/trickster/purge/path"
	// DefaultPprofServerName defines the default Pprof Server Name
	DefaultPprofServerName = "both"
)
