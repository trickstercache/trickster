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
	"context"
	"sync/atomic"
	"testing"
	"time"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

func TestRegisterExternal(t *testing.T) {
	hc := New()
	defer hc.Shutdown()
	er, ok := hc.(interface {
		RegisterExternal(name, description string, s *Status)
	})
	if !ok {
		t.Fatal("health checker does not support external registration")
	}
	st := NewStatus("ext1", "discovered", "", StatusUnchecked, time.Time{}, nil)
	er.RegisterExternal("ext1", "discovered", st)
	if got := hc.Statuses()["ext1"]; got != st {
		t.Fatal("expected external status to be listed")
	}
	// the caller owns transitions
	st.Set(StatusPassing)
	if got := hc.Statuses()["ext1"].Get(); got != StatusPassing {
		t.Fatalf("expected passing, got %d", got)
	}
	// Unregister removes target-less entries too
	hc.Unregister("ext1")
	if _, ok := hc.Statuses()["ext1"]; ok {
		t.Fatal("expected external status to be removed")
	}
	// no-ops must not panic
	er.RegisterExternal("", "x", st)
	er.RegisterExternal("y", "x", nil)
	hc.Unregister("")
}

func TestUnregisterVirtual(t *testing.T) {
	hc := New()
	defer hc.Shutdown()
	hc.RegisterVirtual("v1", "alb")
	if _, ok := hc.Statuses()["v1"]; !ok {
		t.Fatal("expected virtual status to be listed")
	}
	hc.Unregister("v1")
	if _, ok := hc.Statuses()["v1"]; ok {
		t.Fatal("expected virtual status to be removed")
	}
}

// a status that takes over a name retires the active probe registered under it: the probe loop
// has stopped by the time the registration returns, and never runs again
func TestStatusRegistrationRetiresAnActiveProbe(t *testing.T) {
	for _, how := range []string{"external", "virtual"} {
		hc := New().(*healthChecker)
		var probes atomic.Int64
		o := ho.New()
		o.Interval = timeconv.Duration(5 * time.Millisecond)
		o.Timeout = timeconv.Duration(time.Second)
		if _, err := hc.RegisterProbe("member", "test", o, func(context.Context) error {
			probes.Add(1)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for probes.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if probes.Load() == 0 {
			t.Fatal("the probe never ran")
		}
		st := NewStatus("member", "test", "", StatusPassing, time.Time{}, nil)
		if how == "external" {
			hc.RegisterExternal("member", "test", st)
		} else {
			st = hc.RegisterVirtual("member", "test")
		}
		after := probes.Load()
		hc.mtx.RLock()
		_, stillActive := hc.targets["member"]
		hc.mtx.RUnlock()
		if stillActive {
			t.Errorf("%s: the retired probe is still registered", how)
		}
		time.Sleep(50 * time.Millisecond)
		if got := probes.Load(); got != after {
			t.Errorf("%s: the retired probe ran %d more times", how, got-after)
		}
		if hc.Statuses()["member"] != st {
			t.Errorf("%s: the status that took the name over is not the one reported", how)
		}
		hc.Shutdown()
	}
}
