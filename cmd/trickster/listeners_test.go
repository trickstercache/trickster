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

package main

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tricksterproxy/trickster/pkg/proxy/handlers"
	"github.com/tricksterproxy/trickster/pkg/util/log"
)

func TestListeners(t *testing.T) {

	wg := &sync.WaitGroup{}
	var err error
	go func() {
		wg.Add(1)
		err = startListener("httpListener",
			"", 0, 20, nil, http.NewServeMux(), wg, nil, false, log.ConsoleLogger("info"))
	}()

	time.Sleep(time.Millisecond * 300)
	l := listeners["httpListener"]
	l.listener.Close()
	time.Sleep(time.Millisecond * 100)
	if err == nil {
		t.Error("expected non-nil err")
	}

	go func() {
		wg.Add(1)
		err = startListenerRouter("httpListener2",
			"", 0, 20, nil, "/", http.HandlerFunc(handlers.HandleLocalResponse), wg,
			nil, false, log.ConsoleLogger("info"))
	}()
	time.Sleep(time.Millisecond * 300)
	l = listeners["httpListener2"]
	l.listener.Close()
	time.Sleep(time.Millisecond * 100)
	if err == nil {
		t.Error("expected non-nil err")
	}
}

/*


	// it should not error if config path is not set
	conf, _, err := Load("trickster-test", "0", a)
	if err != nil {
		t.Fatal(err)
	}

*/
