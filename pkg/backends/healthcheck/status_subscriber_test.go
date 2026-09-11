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

package healthcheck

import "testing"

func TestStatusSubscriberRegisterUnregister(t *testing.T) {
	s := &Status{}
	const n = 100
	chs := make([]chan bool, n)
	for i := range chs {
		chs[i] = make(chan bool, 1)
		s.RegisterSubscriber(chs[i])
	}
	if got := len(s.subscribers); got != n {
		t.Fatalf("after %d registers, len(subscribers)=%d, want %d", n, got, n)
	}
	for _, ch := range chs {
		s.UnregisterSubscriber(ch)
	}
	if got := len(s.subscribers); got != 0 {
		t.Fatalf("after %d unregisters, len(subscribers)=%d, want 0", n, got)
	}
}

func TestStatusSubscriberReentrantUnregisterIsSafe(t *testing.T) {
	s := &Status{}
	ch := make(chan bool, 1)
	s.RegisterSubscriber(ch)
	s.UnregisterSubscriber(ch)
	s.UnregisterSubscriber(ch)
	if got := len(s.subscribers); got != 0 {
		t.Fatalf("after register+double-unregister, len(subscribers)=%d, want 0", got)
	}

	other := make(chan bool, 1)
	s.UnregisterSubscriber(other)
	if got := len(s.subscribers); got != 0 {
		t.Fatalf("after unregister of never-registered ch, len(subscribers)=%d, want 0", got)
	}

	s.RegisterSubscriber(ch)
	s.RegisterSubscriber(other)
	s.Set(StatusPassing)
	s.UnregisterSubscriber(ch)
	if got := len(s.subscribers); got != 1 {
		t.Fatalf("after one unregister of two, len(subscribers)=%d, want 1", got)
	}
	s.Set(StatusFailing)
}
