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

package engines

import (
	"net/http"
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	tlo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
)

func TestLogUpstreamRequest(t *testing.T) {
	fileName := t.TempDir() + "/out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &tlo.Options{LogFile: fileName, LogLevel: "debug"}
	log := &tl.SyncLogger{Logger: tl.New(conf)}
	logUpstreamRequest(log, "testBackend", "testType", "testHandler", "testMethod",
		"testPath", "testUserAgent", 200, 0, 1.0)
	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
}

func TestLogDownstreamRequest(t *testing.T) {
	fileName := t.TempDir() + "/out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &tlo.Options{LogFile: fileName, LogLevel: "debug"}
	log := &tl.SyncLogger{Logger: tl.New(conf)}
	r, err := http.NewRequest("get", "http://testBackend", nil)
	if err != nil {
		t.Error(err)
	}

	logDownstreamRequest(log, r)

	if _, err := os.Stat(fileName); err != nil {
		t.Errorf(err.Error())
	}
	log.Close()
}
