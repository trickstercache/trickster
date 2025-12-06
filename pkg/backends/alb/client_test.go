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

package alb

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/tsm"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
)

const invalidPoolMemberCheck = "invalid pool member name [invalid] provided for alb [test]"

func TestHandlers(t *testing.T) {

	a := &ao.Options{
		MechanismName: names.MechanismFR,
		OutputFormat:  providers.Prometheus,
	}
	o := bo.New()
	o.ALBOptions = a

	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if _, ok := cl.Handlers()[providers.ALB]; !ok {
		t.Error("expected alb handler")
	}

	a.MechanismName = names.MechanismFGR
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	a.MechanismName = names.MechanismNLM
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	a.MechanismName = string(tsm.ShortName)
	_, err = NewClient("test", o, nil, nil, nil, types.Lookup{providers.Prometheus: prometheus.NewClient})
	if err != nil {
		t.Error(err)
	}

	a.MechanismName = names.MechanismRR
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

}

func TestDefaultPathConfigs(t *testing.T) {
	m := (&Client{}).DefaultPathConfigs(nil)
	if len(m) != 1 {
		t.Error("expected 1 got", len(m))
	}
}

func TestStartALBPools(t *testing.T) {
	err := StartALBPools(nil, nil)
	if err != nil {
		t.Error(err)
	}
	o := bo.New()
	cl, _ := NewClient("test", o, nil, nil, nil, nil)
	b := backends.Backends{"test": cl}
	err = StartALBPools(b, nil)
	if err == nil || err.Error() != "invalid options" {
		t.Error("expected err for invalid options, got", err)
	}
}

func TestValidateClients(t *testing.T) {
	err := ValidateClients(nil)
	if err != nil {
		t.Error(err)
	}
	o := bo.New()
	a := ao.New()
	a.MechanismName = "rx"
	a.Pool = []string{"invalid"}

	o.ALBOptions = a
	o.Provider = providers.ALB
	_, err = NewClient("test", o, nil, nil, nil, nil)
	if err != errors.ErrUnsupportedMechanism {
		t.Error("expected error for unsupported mechanism")
		return
	}
	a.MechanismName = names.MechanismRR
	cl, err := NewClient("test", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
		return
	}

	b := backends.Backends{"test": cl}
	err = ValidateClients(b)
	if err == nil || err.Error() != invalidPoolMemberCheck {
		t.Errorf("expected %s got %s", invalidPoolMemberCheck, err)
	}

	a.Pool = []string{"test"}
	err = ValidateClients(b)
	if err != nil {
		t.Error(err)
	}

	o.Provider = "invalid"
	err = ValidateClients(b)
	if err != nil {
		t.Error(err)
	}

}

func TestValidateAndStartPool(t *testing.T) {

	o := bo.New()
	o.ALBOptions = nil
	tscl, _ := NewClient("test", o, nil, nil, nil, nil)
	cl := tscl.(*Client)

	err := cl.ValidateAndStartPool(nil, nil)
	if err == nil || err.Error() != "invalid options" {
		t.Error("expected error for invalid options, got ", err)
	}

	a := ao.New()
	o.ALBOptions = a
	b := backends.Backends{"test": cl}

	a.MechanismName = names.MechanismRR
	a.Pool = []string{"invalid"}
	err = cl.ValidateAndStartPool(b, nil)
	if err == nil || err.Error() != invalidPoolMemberCheck {
		t.Error("expected error for invalid pool member name, got", err)
	}

	hcs := healthcheck.StatusLookup{
		"test": &healthcheck.Status{},
	}

	a.Pool = []string{"test"}
	err = cl.ValidateAndStartPool(b, hcs)
	if err != nil {
		t.Error(err)
	}

}
