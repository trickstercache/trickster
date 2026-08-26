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

package accesslog

import (
	"sync"
	"time"
)

// generations tracks the Loggers created during the current config
// (re)load so that a reload can retire the previous config's Loggers
var generations = struct {
	sync.Mutex
	current  []*Logger
	previous []*Logger
}{}

// track is called by NewLogger to register l in the current generation
func track(l *Logger) {
	generations.Lock()
	generations.current = append(generations.current, l)
	generations.Unlock()
}

// BeginGeneration moves the current generation of Loggers to the previous
// set; call before registering routes for a new or reloaded config
func BeginGeneration() {
	generations.Lock()
	generations.previous = append(generations.previous, generations.current...)
	generations.current = nil
	generations.Unlock()
}

// CommitGeneration closes the previous generation of Loggers after the
// provided drain delay; call after a successful config (re)load
func CommitGeneration(delay time.Duration) {
	generations.Lock()
	toClose := generations.previous
	generations.previous = nil
	generations.Unlock()
	if len(toClose) == 0 {
		return
	}
	go func() {
		time.Sleep(delay)
		for _, l := range toClose {
			l.Close()
		}
	}()
}

// AbortGeneration closes any Loggers created during a failed config
// (re)load and restores the previous generation as current
func AbortGeneration() {
	generations.Lock()
	toClose := generations.current
	generations.current = generations.previous
	generations.previous = nil
	generations.Unlock()
	for _, l := range toClose {
		l.Close()
	}
}
