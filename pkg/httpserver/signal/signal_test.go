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

package signal

import (
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

func mockServe(oldConf *config.Config, wg *sync.WaitGroup,
	oldCaches map[string]cache.Cache, args []string, errorFunc func()) error {
	return nil
}

func TestStartHupMonitor(t *testing.T) {

	// passing case for this test is no panics or hangs

	w := httptest.NewRecorder()
	l := logging.StreamLogger(w, level.Warn)
	logger.SetLogger(l)

	StartHupMonitor(nil, nil, nil, nil, mockServe)

	qch := make(chan bool)
	conf := config.NewConfig()
	conf.Resources = &config.Resources{QuitChan: qch}
	StartHupMonitor(conf, nil, nil, nil, mockServe)
	time.Sleep(time.Millisecond * 100)
	qch <- true

	StartHupMonitor(conf, nil, nil, nil, mockServe)
	time.Sleep(time.Millisecond * 100)
	hups <- syscall.SIGHUP
	time.Sleep(time.Millisecond * 100)

	w = httptest.NewRecorder()
	l2 := logging.StreamLogger(w, level.Warn)
	logger.SetLogger(l2)
	l.Close()

	now := time.Unix(1577836800, 0)
	nowMinus1m := time.Now().Add(-1 * time.Minute)
	conf.Main.SetStalenessInfo("../../testdata/test.empty.conf", now, nowMinus1m)
	StartHupMonitor(conf, nil, nil, nil, mockServe)
	time.Sleep(time.Millisecond * 100)
	hups <- syscall.SIGHUP
	time.Sleep(time.Millisecond * 100)

	logger.SetLogger(logging.ConsoleLogger(level.Error))
	l2.Close()
}
