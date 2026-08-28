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
	"testing"
	"time"
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
