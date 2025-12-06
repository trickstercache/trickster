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

// Package app holds application build information
package appinfo

import "os"

// Name is the name of the Application
var Name string

// Version holds the version of the Application
var Version string

// BuildTime is the Time that the Application was Built
var BuildTime string

// GitCommitID holds the Git Commit ID of the current binary/build
var GitCommitID string

// Server is the name, hostname or ip of the server as advertised in HTTP Headers
// By default uses the hostname reported by the kernel
var Server, _ = os.Hostname()

func Set(name, version, buildTime, gitCommitID string) {
	Name = name
	Version = version
	BuildTime = buildTime
	GitCommitID = gitCommitID
}

func SetServer(server string) {
	Server = server
}
