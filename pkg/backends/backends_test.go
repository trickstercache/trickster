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

package backends

import (
	"testing"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/router"
)

func TestBackends(t *testing.T) {

	cl, _ := New("test1", bo.New(), nil, router.NewRouter(), nil)
	o := Backends{"test1": cl}

	c := o.Get("test1")
	if c == nil {
		t.Error("expected non-nil client")
	}

	c = o.Get("invalid")
	if c != nil {
		t.Error("expected nil client")
	}

	cfg := o.GetConfig("test1")
	if cfg == nil {
		t.Error("expected non-nil config")
	}

	cfg = o.GetConfig("invalid")
	if cfg != nil {
		t.Error("expected nil config")
	}

	r := o.GetRouter("test1")
	if r == nil {
		t.Error("expected non-nil router")
	}

	r = o.GetRouter("invalid")
	if r != nil {
		t.Error("expected nil router")
	}
}

func TestIsVirtual(t *testing.T) {

	if ok := IsVirtual("rule"); !ok {
		t.Error("expected true")
	}

	if ok := IsVirtual("prometheus"); ok {
		t.Error("expected false")
	}

}

func TestStartHealthChecks(t *testing.T) {

	// 1: rule / Virtual provider
	o1 := bo.New()
	o1.Provider = "rule"
	c1, _ := New("test1", o1, nil, router.NewRouter(), nil)

	// 2: non-virtual provider with no health check options
	o2 := bo.New()
	c2, _ := New("test2", o2, nil, router.NewRouter(), nil)

	b := Backends{"test1": c1}
	_, err := b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	b = Backends{"test1": c1, "test2": c2}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	o2.HealthCheck = nil
	b = Backends{"test1": c1, "test2": c2}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	o2.HealthCheck = ho.New()
	b = Backends{"test1": c1, "test2": c2}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

	c2a := &testBackend{Backend: c2}
	b = Backends{"test1": c1, "test2": c2a}
	_, err = b.StartHealthChecks(nil)
	if err != nil {
		t.Error(err)
	}

}

type testBackend struct {
	Backend
}

func (tb *testBackend) DefaultHealthCheckConfig() *ho.Options {
	return ho.New()
}

func TestUsesCache(t *testing.T) {
	b := UsesCache("rp")
	if b {
		t.Error("expected false")
	}
}
