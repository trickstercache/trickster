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

package main

import (
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

func TestStartHupMonitor(t *testing.T) {

	// passing case for this test is no panics or hangs

	w := httptest.NewRecorder()
	logger := logging.StreamLogger(w, "WARN")

	startHupMonitor(nil, nil, nil, nil, nil)

	qch := make(chan bool)
	conf := config.NewConfig()
	conf.Resources = &config.Resources{QuitChan: qch}
	startHupMonitor(conf, nil, logger, nil, nil)
	time.Sleep(time.Millisecond * 100)
	qch <- true

	startHupMonitor(conf, nil, logger, nil, nil)
	time.Sleep(time.Millisecond * 100)
	hups <- syscall.SIGHUP
	time.Sleep(time.Millisecond * 100)

	logger.Close()

	w = httptest.NewRecorder()
	logger = logging.StreamLogger(w, "WARN")

	now := time.Unix(1577836800, 0)
	nowMinus1m := time.Now().Add(-1 * time.Minute)
	conf.Main.SetStalenessInfo("../../testdata/test.empty.conf", now, nowMinus1m)
	startHupMonitor(conf, nil, logger, nil, nil)
	time.Sleep(time.Millisecond * 100)
	hups <- syscall.SIGHUP
	time.Sleep(time.Millisecond * 100)
}
