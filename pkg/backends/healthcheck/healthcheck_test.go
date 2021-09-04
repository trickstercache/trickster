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
	"net/http"
	"testing"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
)

func TestNew(t *testing.T) {

	hc := New()
	if hc == nil {
		t.Error("expected non-nil")
	}
}

func TestSubscribe(t *testing.T) {
	const expected = 1
	hc := New().(*healthChecker)
	ch := make(chan bool)
	hc.Subscribe(ch)
	if len(hc.subscribers) != expected {
		t.Errorf("expected %d got %d", expected, len(hc.subscribers))
	}
}

func TestShutdown(t *testing.T) {
	hc := New().(*healthChecker)
	hc.targets = Lookup{"test": &target{}}
	ch := make(chan bool, 1)
	hc.Subscribe(ch)
	hc.Shutdown()
	val := <-ch
	if !val {
		t.Error("expected true")
	}
}

func TestRegister(t *testing.T) {
	hc := New().(*healthChecker)
	o := ho.New()
	o.IntervalMS = 500
	_, err := hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}
	target := hc.targets["test"]
	target.Start()
	target.Stop()
	_, err = hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}
	o.Body = "test-body"
	_, err = hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}
	_, err = hc.Register("test", "test", nil, http.DefaultClient, nil)
	if err != ho.ErrNoOptionsProvided {
		t.Errorf("expected %v got %v", ho.ErrNoOptionsProvided, err)
	}
}

func TestUnregister(t *testing.T) {
	hc := New().(*healthChecker)
	o := ho.New()
	o.IntervalMS = 500
	_, err := hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}
	hc.Unregister("")
	hc.Unregister("test")
	if _, ok := hc.targets["test"]; ok {
		t.Error("expected false")
	}
}

func TestStatus(t *testing.T) {
	hc := New().(*healthChecker)
	o := ho.New()
	o.IntervalMS = 500
	_, err := hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}

	s := hc.Status("")
	if s != nil {
		t.Error("expected nil got ", s)
	}

	s = hc.Status("test")
	if s == nil {
		t.Error("expected non-nil status")
	}

	s = hc.Status("test-missing")
	if s != nil {
		t.Error("expected nil got ", s)
	}
}

func TestStatuses(t *testing.T) {

	hc := New().(*healthChecker)
	o := ho.New()
	o.IntervalMS = 500
	_, err := hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}

	s := hc.Statuses()
	if len(s) != 1 {
		t.Errorf("expected %d got %d", 1, len(s))
	}
}

func TestHealthCheckerProbe(t *testing.T) {

	hc := New().(*healthChecker)
	o := ho.New()

	ts := newTestServer(200, "OK", map[string]string{})
	r, _ := http.NewRequest("GET", ts.URL+"/", nil)

	_, err := hc.Register("test", "test", o, http.DefaultClient, nil)
	if err != nil {
		t.Error(err)
	}

	target := hc.targets["test"]
	target.baseRequest = r
	target.ctx = context.Background()

	s := hc.Probe("")
	if s != nil {
		t.Error("expected nil got ", s)
	}
	s = hc.Probe("test-missing")
	if s != nil {
		t.Error("expected nil got ", s)
	}
	s = hc.Probe("test")
	if s == nil {
		t.Error("expected non-nil")
	}
}
