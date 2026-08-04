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

package daemon

import (
	"slices"
	"sync"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"
)

// withStubHupDelegate swaps hupDelegate for the duration of the test and
// returns the call recorder. The original delegate is restored on cleanup.
func withStubHupDelegate(t *testing.T) *hupCallRecorder {
	t.Helper()
	rec := &hupCallRecorder{}
	prev := hupDelegate
	hupDelegate = rec.record
	t.Cleanup(func() { hupDelegate = prev })
	return rec
}

type hupCall struct {
	source string
	args   []string
}

type hupCallRecorder struct {
	mu    sync.Mutex
	calls []hupCall
}

func (r *hupCallRecorder) record(_ *instance.ServerInstance, source string, args ...string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, hupCall{source: source, args: slices.Clone(args)})
	return true, nil
}

func (r *hupCallRecorder) snapshot() []hupCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func TestNewHupFuncForwardsArgs(t *testing.T) {
	rec := withStubHupDelegate(t)

	si := &instance.ServerInstance{Listeners: listener.NewGroup()}
	args := []string{"-config", "/custom/path/trickster.yaml"}

	reloader := newHupFunc(si, args)
	if _, err := reloader("test"); err != nil {
		t.Fatalf("reloader returned error: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 hup call, got %d", len(calls))
	}
	if calls[0].source != "test" {
		t.Errorf("source = %q, want %q", calls[0].source, "test")
	}
	if !slices.Equal(calls[0].args, args) {
		t.Errorf("args = %v, want %v", calls[0].args, args)
	}
}

// TestNewHupFuncPostReloadPreservesArgs simulates the bug scenario: a reload
// happens, a fresh hupFunc is created (as Hup does after ApplyConfig), and the
// re-registered hupFunc is invoked. It must still forward the original -config
// args, not silently fall back to the default config path. This is the
// regression test for the POST /trickster/config/reload after-SIGHUP bug.
func TestNewHupFuncPostReloadPreservesArgs(t *testing.T) {
	rec := withStubHupDelegate(t)

	si := &instance.ServerInstance{Listeners: listener.NewGroup()}
	args := []string{"-config", "/custom/path/trickster.yaml"}

	initial := newHupFunc(si, args)
	if _, err := initial("sighup"); err != nil {
		t.Fatalf("initial reloader returned error: %v", err)
	}

	// Mimic what Hup does after a successful reload: build a new hupFunc to
	// re-register with the new router. Before the fix this closure dropped
	// args; after the fix it must close over the same args.
	reregistered := newHupFunc(si, args)
	if _, err := reregistered("mgmt-api"); err != nil {
		t.Fatalf("re-registered reloader returned error: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 hup calls, got %d", len(calls))
	}
	if !slices.Equal(calls[1].args, args) {
		t.Errorf("post-reload args = %v, want %v -- POST /trickster/config/reload "+
			"would read from the default config path instead of -config",
			calls[1].args, args)
	}
}

func TestNewHupFuncNilArgs(t *testing.T) {
	rec := withStubHupDelegate(t)

	si := &instance.ServerInstance{Listeners: listener.NewGroup()}
	reloader := newHupFunc(si, nil)
	if _, err := reloader("test"); err != nil {
		t.Fatalf("reloader returned error: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 hup call, got %d", len(calls))
	}
	if len(calls[0].args) != 0 {
		t.Errorf("args = %v, want empty", calls[0].args)
	}
}
