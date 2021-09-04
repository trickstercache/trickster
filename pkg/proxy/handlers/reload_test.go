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

package handlers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	"github.com/trickstercache/trickster/v2/pkg/cache"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
)

func TestReloadHandleFunc(t *testing.T) {

	var emptyFunc = func(*config.Config, *sync.WaitGroup, *tl.Logger,
		map[string]cache.Cache, []string, func()) error {
		return nil
	}

	testFile := t.TempDir() + "/trickster_test_config.conf"

	tml, err := os.ReadFile("../../../testdata/test.empty.conf")
	if err != nil {
		t.Error(err)
	}

	err = os.WriteFile(testFile, tml, 0666)
	if err != nil {
		t.Error(err)
	}

	cfg, _, _ := config.Load("testing", "testing", []string{"-config", testFile})
	cfg.ReloadConfig.RateLimitMS = 0
	log := tl.ConsoleLogger("info")
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "/", nil)

	f := ReloadHandleFunc(emptyFunc, cfg, nil, log, nil, nil)
	f(w, r)
	os.Remove(testFile)
	time.Sleep(time.Millisecond * 500)
	os.WriteFile(testFile, []byte(string(tml)), 0666)
	time.Sleep(time.Millisecond * 500)
	f(w, r)
}
