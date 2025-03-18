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
	"os"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	tlo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
)

func TestLogUpstreamRequest(t *testing.T) {
	fileName := t.TempDir() + "/out.log"
	// it should create a logger that outputs to a log file ("out.test.log")
	conf := config.NewConfig()
	conf.Main = &config.MainConfig{InstanceID: 0}
	conf.Logging = &tlo.Options{LogFile: fileName, LogLevel: "debug"}
	logger := logging.New(conf)
	logger.SetLogAsynchronous(false)
	logUpstreamRequest(logger, "testBackend", "testType", "testHandler", "testMethod",
		"testPath", "testUserAgent", 200, 0, 1.0)
	if _, err := os.Stat(fileName); err != nil {
		t.Error(err)
	}
	logger.Close()
}
