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

package engines

import (
	"net/http"
	"os"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/log"
)

func TestLogUpstreamRequest(t *testing.T) {
	fileName := "out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Main = &config.MainConfig{InstanceID: 0}
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	logger := log.New(config.Logging, config.Main.InstanceID)
	logUpstreamRequest("testOrigin", "testType", "testHandler", "testMethod", "testPath", "testUserAgent", 200, 0, 1.0, logger)
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}

func TestLogDownstreamRequest(t *testing.T) {
	fileName := "out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	config.Config = config.NewConfig()
	config.Main = &config.MainConfig{InstanceID: 0}
	config.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	logger := log.New(config.Logging, config.Main.InstanceID)

	r, err := http.NewRequest("get", "http://testOrigin", nil)
	if err != nil {
		t.Error(err)
	}

	logDownstreamRequest(r, logger)

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	logger.Close()
	os.Remove(fileName)
}
