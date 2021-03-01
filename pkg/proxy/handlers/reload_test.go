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

package handlers

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/pkg/cache"
	"github.com/tricksterproxy/trickster/pkg/config"
	tl "github.com/tricksterproxy/trickster/pkg/util/log"
)

func TestReloadHandleFunc(t *testing.T) {

	var emptyFunc = func(*config.Config, *sync.WaitGroup, *tl.Logger,
		map[string]cache.Cache, []string, func()) error {
		return nil
	}

	testFile := fmt.Sprintf("trickster_test_config.%d.conf", time.Now().UnixNano())

	tml, err := ioutil.ReadFile("../../../testdata/test.empty.conf")
	if err != nil {
		t.Error(err)
	}

	err = ioutil.WriteFile(testFile, tml, 0666)
	if err != nil {
		t.Error(err)
	}
	defer os.Remove(testFile)

	cfg, _, _ := config.Load("testing", "testing", []string{"-config", testFile})
	cfg.ReloadConfig.RateLimitSecs = 0
	log := tl.ConsoleLogger("info")
	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "/", nil)

	f := ReloadHandleFunc(emptyFunc, cfg, nil, log, nil, nil)
	f(w, r)
	os.Remove(testFile)
	time.Sleep(time.Millisecond * 500)
	ioutil.WriteFile(testFile, []byte(string(tml)), 0666)
	time.Sleep(time.Millisecond * 500)
	f(w, r)
}
