/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package main

import (
	"testing"
)

func TestNewLogger(t *testing.T) {
	testCases := []string{
		"debug",
		"info",
		"warn",
		"error",
		"none",
	}
	// it should create a logger for each level
	for _, tc := range testCases {
		t.Run(tc, func(t *testing.T) {
			newLogger(LoggingConfig{LogLevel: tc}, tc)
		})
	}
}

func TestNewLogger_LogFile(t *testing.T) {
	// it should create a logger that outputs to a log file ("out.test.log")
	newLogger(LoggingConfig{LogFile: "out.log"}, "test")
}
