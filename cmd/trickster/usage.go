/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package main

import (
	"fmt"

	"github.com/Comcast/trickster/internal/runtime"
)

const usageText = `
Trickster Usage:

 You must provide -version, -config or both -origin-url and -origin-type.

 Print Version Info:
 trickster -version

 Using a configuration file:
  trickster -config /path/to/file.conf [-log-level DEBUG|INFO|WARN|ERROR] [-proxy-port 8480] [-metrics-port 8481]

 Using origin-url and origin-type:
  trickster -origin-url https://example.com -origin-type reverseproxycache [-log-level DEBUG|INFO|WARN|ERROR] [-proxy-port 8480] [-metrics-port 8481]

------

 Simple HTTP Reverse Proxy Cache listening on 8080:
   trickster -origin-url https://example.com/ -origin-type reverseproxycache -proxy-port 8080

 Simple Prometheus Accelerator listening on 9090 (default port) with Debugging:
   trickster -origin-url http://prometheus.example.com:9090/ -origin-type prometheus -log-level DEBUG

 Simple InfluxDB Accelerator listening on 8086:
   trickster -origin-url http://influxdb.example.com:8086/ -origin-type influxdb -proxy-port 8086

 Simple ClickHouse Accelerator listening on 8123:
   trickster -origin-url http://clickhouse.example.com:8123/ -origin-type clickhouse -proxy-port 8123

------

Trickster listens on port 8480 by default. Set in a config file, or override using -proxy-port.

Default log level is INFO. Set in a config file, or override with -log-level.

The configuration file is much more robust than the command line arguments, and the example file
is well-documented. We also have docker images on DockerHub, as well as Kubernetes in our GitHub
repository, Charts on Helm Hub, and standalone binaries on our GitHub releases page.

Thank you for using and contributing to Open Source Software!

https://github.com/tricksterproxy/trickster
`

func version() string {
	return fmt.Sprintf("Trickster version: %s, buildInfo: %s %s, goVersion: %s © 2018 Comcast",
		runtime.ApplicationVersion,
		applicationBuildTime, applicationGitCommitID,
		applicationGoVersion,
	)
}

func printVersion() {
	fmt.Println(version())
}

func printUsage() {
	fmt.Println()
	fmt.Println(version())
	fmt.Println(usageText)
}
