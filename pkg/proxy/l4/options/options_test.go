/*
 * Copyright 2026 The Trickster Authors
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

package options

import (
	"errors"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
)

func TestOptionsDefaultsAndEffectiveValues(t *testing.T) {
	var nilOpts *Options
	if nilOpts.Connect() != time.Duration(DefaultConnectTimeout) {
		t.Errorf("nil Connect = %v", nilOpts.Connect())
	}
	if nilOpts.Idle() != 0 {
		t.Errorf("nil Idle = %v", nilOpts.Idle())
	}
	if nilOpts.UDPIdle() != time.Duration(DefaultUDPIdleTimeout) {
		t.Errorf("nil UDPIdle = %v", nilOpts.UDPIdle())
	}
	if nilOpts.Clone() != nil || nilOpts.Validate() != nil {
		t.Error("nil options must clone to nil and validate")
	}
	o := New()
	if o.Connect() != time.Duration(DefaultConnectTimeout) || o.Idle() != 0 {
		t.Errorf("defaults: connect %v idle %v", o.Connect(), o.Idle())
	}
	o.ConnectTimeout = timeconv.Duration(time.Second)
	o.IdleTimeout = timeconv.Duration(2 * time.Second)
	if o.Connect() != time.Second || o.Idle() != 2*time.Second || o.UDPIdle() != 2*time.Second {
		t.Errorf("effective: connect %v idle %v udp %v", o.Connect(), o.Idle(), o.UDPIdle())
	}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	c := o.Clone()
	if !c.Equal(o) || c == o {
		t.Error("clone must be equal and distinct")
	}
	c.IdleTimeout = 0
	if c.Equal(o) || o.Equal(nil) || !nilOpts.Equal(nil) {
		t.Error("equality mismatch")
	}
	o.IdleTimeout = -1
	if err := o.Validate(); !errors.Is(err, ErrNegativeTimeout) {
		t.Errorf("negative idle timeout validated: %v", err)
	}
	o.IdleTimeout, o.ConnectTimeout = 0, -1
	if err := o.Validate(); !errors.Is(err, ErrNegativeTimeout) {
		t.Errorf("negative connect timeout validated: %v", err)
	}
}
