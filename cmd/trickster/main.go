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
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/trickstercache/trickster/v2/pkg/appinfo"
	"github.com/trickstercache/trickster/v2/pkg/appinfo/usage"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	"github.com/trickstercache/trickster/v2/pkg/httpserver"
)

var (
	applicationGitCommitID string
	applicationBuildTime   string
)

const (
	applicationName    = "trickster"
	applicationVersion = "2.0.0-beta2"
)

func main() {
	err := daemon()
	if err != nil {
		os.Exit(1)
	}
}

func daemon() error {
	wg := &sync.WaitGroup{}
	appinfo.SetAppInfo(applicationName, applicationVersion,
		applicationBuildTime, applicationGitCommitID)

	// Load Config
	conf, err := config.Load(appinfo.Name, appinfo.Version, os.Args[1:])
	if err != nil {
		fmt.Println("\nERROR: Could not load configuration:", err.Error())
		if conf != nil && conf.Flags != nil && conf.Flags.ValidateConfig {
			usage.PrintUsage()
		}
	}
	if conf == nil {
		return errors.ErrInvalidOptions
	}

	// Serve with Config
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	err = httpserver.Serve(conf, wg, nil, nil, exitFatal)
	if err != nil {
		return err
	}

	select {
	case sig := <-quit:
		fmt.Println("signal received", sig)
		// more cases to be added
	}

	return nil
}

func exitFatal() {
	os.Exit(1)
}
