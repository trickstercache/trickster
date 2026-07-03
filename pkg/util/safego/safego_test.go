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

package safego

import (
	"bytes"
	"sync"
	"testing"
)

func TestRunRecoversPanicInvokesHandler(t *testing.T) {
	var got any
	var stack []byte
	Run(func(r any, s []byte) {
		got = r
		stack = s
	}, func() {
		panic("boom")
	})
	if got != "boom" {
		t.Errorf("handler.r = %v, want \"boom\"", got)
	}
	if !bytes.Contains(stack, []byte("safego.Run")) {
		t.Errorf("handler.stack missing safego.Run frame; got:\n%s", stack)
	}
}

func TestRunNoPanicHandlerNotInvoked(t *testing.T) {
	called := false
	Run(func(_ any, _ []byte) { called = true }, func() {})
	if called {
		t.Errorf("handler invoked when fn did not panic")
	}
}

func TestGoSpawnsGoroutineAndRecovers(t *testing.T) {
	var wg sync.WaitGroup
	var got any
	wg.Add(1)
	Go(func(r any, _ []byte) {
		got = r
		wg.Done()
	}, func() {
		panic(42)
	})
	wg.Wait()
	if got != 42 {
		t.Errorf("handler.r = %v, want 42", got)
	}
}

func TestRunNoopHandlerStillRecovers(t *testing.T) {
	Run(func(_ any, _ []byte) {}, func() { panic("ignored") })
}
