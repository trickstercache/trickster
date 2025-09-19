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

// Package main is the main package for the Trickster application
package main

import (
	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/daemon"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

// application variables set at build time via go build's -ldflags
var (
	applicationGitCommitID string
	applicationBuildTime   string
	applicationVersion     string
)

const (
	applicationName = "trickster"
)

func main() {
	appinfo.Set(applicationName, applicationVersion,
		applicationBuildTime, applicationGitCommitID)
	err := daemon.Start()
	if err != nil {
		logger.Fatal(1, "trickster daemon failed to start",
			logging.Pairs{"error": err})
	}
}
