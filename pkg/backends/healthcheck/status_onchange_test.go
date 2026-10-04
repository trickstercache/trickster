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

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/lb"
)

var (
	_ lb.Health   = (*Status)(nil)
	_ lb.Notifier = (*Status)(nil)
)

type transition struct{ prev, next int32 }

func TestStatusOnChangeReportsTransitions(t *testing.T) {
	s := &Status{}
	var got []transition
	sub := s.OnChange(func(prev, next int32) { got = append(got, transition{prev, next}) })

	s.Set(StatusPassing)
	s.Set(StatusPassing) // not a change
	s.Set(StatusFailing)
	want := []transition{{StatusUnchecked, StatusPassing}, {StatusPassing, StatusFailing}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("transitions = %v, want %v", got, want)
	}

	sub.Unsubscribe()
	sub.Unsubscribe()
	s.Set(StatusPassing)
	if len(got) != len(want) {
		t.Errorf("an unsubscribed callback ran: %v", got)
	}
	if len(s.onChange) != 0 {
		t.Errorf("%d subscriptions left behind", len(s.onChange))
	}
}

// the callback runs outside the Status lock: it can read the Status, register, and
// unsubscribe itself without deadlocking
func TestStatusOnChangeCallbackMayReenter(t *testing.T) {
	s := &Status{}
	var sub lb.Subscription
	var calls, late int
	sub = s.OnChange(func(_, _ int32) {
		calls++
		_ = s.Detail()
		s.SetDetail("seen")
		s.OnChange(func(_, _ int32) { late++ })
		sub.Unsubscribe()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Set(StatusPassing)
		s.Set(StatusFailing)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Set deadlocked on a re-entrant callback")
	}
	if calls != 1 || late != 1 {
		t.Errorf("calls = %d, registered-during-callback calls = %d", calls, late)
	}
}

// a panicking callback reaches neither its siblings, the channel subscribers, nor the caller
func TestStatusOnChangePanicIsIsolated(t *testing.T) {
	s := NewStatus("panicky", "", "", StatusUnchecked, time.Time{}, nil)
	var before, after int
	s.OnChange(func(_, _ int32) { before++ })
	s.OnChange(func(_, _ int32) { panic("callback blew up") })
	s.OnChange(func(_, _ int32) { after++ })
	ch := make(chan bool, 1)
	s.RegisterSubscriber(ch)

	s.Set(StatusPassing)
	s.Set(StatusFailing)
	if before != 2 || after != 2 {
		t.Errorf("sibling callbacks ran %d and %d times, want 2 and 2", before, after)
	}
	select {
	case <-ch:
	default:
		t.Error("the channel subscriber was not notified")
	}
	if s.Get() != StatusFailing {
		t.Error("the status was not stored")
	}
}

// Unsubscribe racing Set neither deadlocks nor lets the callback run once it has returned
// and an in-flight Set has drained
func TestStatusOnChangeUnsubscribeRacesSet(t *testing.T) {
	for range 200 {
		s := &Status{}
		var stopped atomic.Bool
		var lateCalls atomic.Int32
		entered := make(chan struct{}, 1)
		sub := s.OnChange(func(_, _ int32) {
			select {
			case entered <- struct{}{}:
			default:
			}
			if stopped.Load() {
				lateCalls.Add(1)
			}
		})
		var wg sync.WaitGroup
		wg.Go(func() {
			for i := range 100 {
				s.Set(int32(i%2) - 1)
			}
		})
		<-entered
		sub.Unsubscribe()
		wg.Wait()
		stopped.Store(true)
		s.Set(StatusPassing)
		s.Set(StatusFailing)
		if lateCalls.Load() != 0 {
			t.Fatal("a callback ran after Unsubscribe and the racing Set had both returned")
		}
	}
}
