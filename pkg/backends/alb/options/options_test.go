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

package options

import (
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

type testOptions1 struct {
	Backends map[string]*testOptions2 `yaml:"backends,omitempty"`
}

type testOptions2 struct {
	Alb *Options `yaml:"alb,omitempty"`
}

func fromYAML(conf string) (*Options, error) {

	to := &testOptions1{}
	err := yaml.Unmarshal([]byte(conf), to)
	if err != nil {
		return nil, err
	}

	for _, v := range to.Backends {
		if v != nil && v.Alb != nil {
			return v.Alb, nil
		}
	}
	return nil, nil
}

func TestNew(t *testing.T) {

	o := New()
	if o == nil {
		t.Error("expected non-nil")
	}

}

func TestClone(t *testing.T) {

	o := New()
	o.Pool = []string{"test"}
	o.FGRStatusCodes = []int{200}
	o.FgrCodesLookup = sets.New([]int{200})
	require.NotNil(t, o)
	co := o.Clone()

	if len(co.Pool) != 1 || co.Pool[0] != "test" {
		t.Error("clone mismatch")
	}
	if len(co.FGRStatusCodes) != 1 || co.FGRStatusCodes[0] != 200 {
		t.Error("status codes mismatch")
	}
	if len(co.FgrCodesLookup) != 1 || !co.FgrCodesLookup.Contains(200) {
		t.Error("fgr lookup mismatch")
	}
}

func TestInitialize(t *testing.T) {

	// Test with nil options - this should panic, so we don't test it
	// since Initialize() is a method on the struct, calling it on nil will panic

	// Test with empty options
	o := New()
	err := o.Initialize()
	if err != nil {
		t.Error(err)
	}

	// Test with TSM mechanism
	o, err = fromYAML(testTOML)
	if err != nil {
		t.Error(err)
	}
	err = o.Initialize()
	if err != nil {
		t.Error(err)
	}
	if o.OutputFormat != "prometheus" {
		t.Error("expected output_format to be set to prometheus")
	}

	// Test with FGR mechanism
	o, err = fromYAML(testFGR)
	if err != nil {
		t.Error(err)
	}
	err = o.Initialize()
	if err != nil {
		t.Error("failed to set defaults")
	}
	if o.FgrCodesLookup == nil || !o.FgrCodesLookup.Contains(200) || !o.FgrCodesLookup.Contains(201) {
		t.Error("expected FGR codes lookup to be set")
	}

	// Test with tsmerge mechanism name (should be shortened to tsm)
	o = New()
	o.MechanismName = "tsmerge"
	o.OutputFormat = "prometheus"
	err = o.Initialize()
	if err != nil {
		t.Error(err)
	}
	if o.MechanismName != "tsm" {
		t.Error("expected mechanism name to be shortened to tsm")
	}

}

func TestErrInvalidALBOptions(t *testing.T) {
	err := NewErrInvalidALBOptions("test")
	var e *InvalidALBOptionsError
	ok := errors.As(err, &e)
	if !ok {
		t.Error("invalid type assertion")
	}
}
