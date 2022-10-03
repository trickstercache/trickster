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
	"os"
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/runtime"
)

var (
	applicationGitCommitID string
	applicationBuildTime   string
)

const (
	applicationName    = "trickster"
	applicationVersion = "2.0.0-beta2"
)

var wg = &sync.WaitGroup{}

var exitFunc func() = exitFatal

func main() {
	runtime.ApplicationName = applicationName
	runtime.ApplicationVersion = applicationVersion
	runConfig(nil, wg, nil, nil, os.Args[1:], exitFunc)
	wg.Wait()
}

func exitFatal() {
	os.Exit(1)
}
