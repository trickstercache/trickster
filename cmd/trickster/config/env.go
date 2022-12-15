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

import (
	"os"
	"strconv"
)

const (
	// Environment variables
	evOriginURL   = "TRK_ORIGIN_URL"
	evProvider    = "TRK_ORIGIN_TYPE"
	evProxyPort   = "TRK_PROXY_PORT"
	evMetricsPort = "TRK_METRICS_PORT"
	evLogLevel    = "TRK_LOG_LEVEL"
)

func (c *Config) loadEnvVars() {
	// Origin
	if x := os.Getenv(evOriginURL); x != "" {
		c.providedOriginURL = x
	}

	if x := os.Getenv(evProvider); x != "" {
		c.providedProvider = x
	}

	// Proxy Port
	if x := os.Getenv(evProxyPort); x != "" {
		if y, err := strconv.ParseInt(x, 10, 32); err == nil {
			c.Frontend.ListenPort = int(y)
		}
	}

	// Metrics Port
	if x := os.Getenv(evMetricsPort); x != "" {
		if y, err := strconv.ParseInt(x, 10, 32); err == nil {
			c.Metrics.ListenPort = int(y)
		}
	}

	// LogLevel
	if x := os.Getenv(evLogLevel); x != "" {
		c.Logging.LogLevel = x
	}

}
