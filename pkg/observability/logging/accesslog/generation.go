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

var generations = struct {
	sync.Mutex
	current  []*Logger
	previous []*Logger
}{}

func track(l *Logger) {
	generations.Lock()
	generations.current = append(generations.current, l)
	generations.Unlock()
}

// BeginGeneration starts tracking loggers for a new configuration.
func BeginGeneration() {
	generations.Lock()
	generations.previous = append(generations.previous, generations.current...)
	generations.current = nil
	generations.Unlock()
}

// CommitGeneration closes the previous logger generation after delay.
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

// AbortGeneration closes new loggers and restores the previous generation.
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
