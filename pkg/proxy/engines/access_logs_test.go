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

package engines

import (
	"net/http"
	"os"
	"testing"

	"github.com/tricksterproxy/trickster/pkg/config"
	"github.com/tricksterproxy/trickster/pkg/logging"
	tl "github.com/tricksterproxy/trickster/pkg/logging"
)

func TestLogUpstreamRequest(t *testing.T) {
	fileName := "out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	log := &logging.SyncLogger{Logger: tl.New(conf)}
	logUpstreamRequest(log, "testOrigin", "testType", "testHandler", "testMethod",
		"testPath", "testUserAgent", 200, 0, 1.0)
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}

func TestLogDownstreamRequest(t *testing.T) {
	fileName := "out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &config.LoggingConfig{LogFile: fileName, LogLevel: "debug"}
	log := &logging.SyncLogger{Logger: tl.New(conf)}
	r, err := http.NewRequest("get", "http://testOrigin", nil)
	if err != nil {
		t.Error(err)
	}

	logDownstreamRequest(log, r)

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
	os.Remove(fileName)
}
