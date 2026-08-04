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

// Package safego spawns goroutines whose panics must not crash the
// process. Callers pass a PanicHandler closure; the package only owns
// the `go { defer recover() { handler(r) } }` boilerplate.
package safego

import "runtime/debug"

// PanicHandler is invoked on a recovered panic with the value and stack.
type PanicHandler func(r any, stack []byte)

// Go starts fn in a new goroutine, recovering panics into handler. Use
// Run when fn is already inside a goroutine body.
func Go(handler PanicHandler, fn func()) {
	go Run(handler, fn)
}

// Run executes fn synchronously, recovering panics into handler.
func Run(handler PanicHandler, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			handler(r, debug.Stack())
		}
	}()
	fn()
}
