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

package signaling

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// guardSignals keeps a test-owned channel registered for the signals Wait
// handles, so the process default disposition (terminate on SIGINT/SIGTERM,
// SIGHUP) stays disabled for the entire test, even in the windows before Wait
// registers its own channel and after Wait unregisters it.
func guardSignals(t *testing.T) {
	t.Helper()
	guard := make(chan os.Signal, 16)
	signal.Notify(guard, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	t.Cleanup(func() {
		signal.Stop(guard)
		// drain anything still queued so a late delivery can't be observed
		// by a subsequent test
		for {
			select {
			case <-guard:
			default:
				return
			}
		}
	})
}

// raiseUntil sends sig to this process repeatedly until done is closed or the
// deadline elapses. Signal delivery races with Wait's own signal.Notify
// registration, so a single send is not reliable.
func raiseUntil(t *testing.T, sig syscall.Signal, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return
		default:
		}
		if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
			t.Errorf("could not send %v: %v", sig, err)
			return
		}
		select {
		case <-done:
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %v to be handled", sig)
}

func TestWaitContextCancel(t *testing.T) {
	guardSignals(t)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int64
	done := make(chan struct{})
	go func() {
		Wait(ctx, func(string) (bool, error) {
			calls.Add(1)
			return true, nil
		})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after context cancellation")
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("reloader calls = %d, want 0", got)
	}
}

func TestWaitSIGHUPReloadsThenSIGTERMReturns(t *testing.T) {
	guardSignals(t)
	ctx := t.Context()

	var sources atomic.Value
	sources.Store("")
	reloaded := make(chan struct{})
	var once atomic.Bool
	done := make(chan struct{})
	go func() {
		Wait(ctx, func(source string) (bool, error) {
			sources.Store(source)
			if once.CompareAndSwap(false, true) {
				close(reloaded)
			}
			return true, nil
		})
		close(done)
	}()

	raiseUntil(t, syscall.SIGHUP, reloaded)
	select {
	case <-reloaded:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not invoke the reloader")
	}
	if got := sources.Load().(string); got != "sighup" {
		t.Errorf("reload source = %q, want %q", got, "sighup")
	}

	select {
	case <-done:
		t.Fatal("Wait returned on SIGHUP; it should keep waiting")
	default:
	}

	raiseUntil(t, syscall.SIGTERM, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after SIGTERM")
	}
}

func TestWaitSIGINTReturns(t *testing.T) {
	guardSignals(t)
	ctx := t.Context()

	done := make(chan struct{})
	go func() {
		Wait(ctx, func(string) (bool, error) { return true, nil })
		close(done)
	}()

	raiseUntil(t, syscall.SIGINT, done)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after SIGINT")
	}
}
