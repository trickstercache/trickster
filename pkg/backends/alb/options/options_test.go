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
	"os"
	"testing"

	ur "github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/ur/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/names"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
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

func fromYAMLFile(t *testing.T, path string) *Options {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	o, err := fromYAML(string(b))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return o
}

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil")
	}
}

func TestClone(t *testing.T) {
	o := New()
	o.Pool = Members("test")
	o.FGRStatusCodes = []int{200}
	o.FgrCodesLookup = sets.New([]int{200})
	require.NotNil(t, o)
	co := o.Clone()

	if len(co.Pool) != 1 || co.Pool[0].Name != "test" {
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
	err := o.Initialize("")
	if err != nil {
		t.Error(err)
	}

	// Test with TSM mechanism
	o = fromYAMLFile(t, "testdata/tsm.yaml")
	err = o.Initialize("")
	if err != nil {
		t.Error(err)
	}
	if o.OutputFormat != "prometheus" {
		t.Error("expected output_format to be set to prometheus")
	}

	// Test with FGR mechanism
	o = fromYAMLFile(t, "testdata/fgr.yaml")
	err = o.Initialize("")
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
	err = o.Initialize("")
	if err != nil {
		t.Error(err)
	}
	if o.MechanismName != names.MechanismTSM {
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

func TestCloneWithUserRouter(t *testing.T) {
	o := New()
	o.Pool = Members("a", "b")
	o.UserRouter = &ur.Options{DefaultBackend: "prom1"}
	co := o.Clone()
	require.NotNil(t, co.UserRouter)
	require.Equal(t, "prom1", co.UserRouter.DefaultBackend)
	co.UserRouter.DefaultBackend = "changed"
	require.Equal(t, "prom1", o.UserRouter.DefaultBackend)
}

func TestInitializeDeprecatedFGRAndDefaultOutputFormat(t *testing.T) {
	o := New()
	o.MechanismName = names.MechanismFGR
	o.FGRStatusCodes = []int{200, 204}
	require.NoError(t, o.Initialize(""))
	require.Equal(t, []int{200, 204}, o.FGROptions.StatusCodes)
	require.True(t, o.FgrCodesLookup.Contains(200))
	require.True(t, o.FgrCodesLookup.Contains(204))

	o = New()
	o.MechanismName = names.MechanismTSM
	require.NoError(t, o.Initialize(""))
	require.Equal(t, "prometheus", o.OutputFormat)
}

func TestValidate(t *testing.T) {
	t.Run("user router required", func(t *testing.T) {
		o := New()
		o.MechanismName = names.MechanismUR
		ok, err := o.Validate()
		require.False(t, ok)
		require.ErrorIs(t, err, ErrUserRouterRequired)
	})

	t.Run("user router ok", func(t *testing.T) {
		o := New()
		o.MechanismName = names.MechanismUR
		o.UserRouter = &ur.Options{DefaultBackend: "prom1"}
		ok, err := o.Validate()
		require.True(t, ok)
		require.NoError(t, err)
	})

	t.Run("invalid tsm output format", func(t *testing.T) {
		o := New()
		o.MechanismName = names.MechanismTSM
		o.OutputFormat = "not-a-provider"
		ok, err := o.Validate()
		require.False(t, ok)
		require.ErrorIs(t, err, ErrInvalidOutputFormat)
	})

	t.Run("valid tsm output format", func(t *testing.T) {
		o := New()
		o.MechanismName = names.MechanismTSM
		o.OutputFormat = "prometheus"
		ok, err := o.Validate()
		require.True(t, ok)
		require.NoError(t, err)
	})

	t.Run("output format only for tsm", func(t *testing.T) {
		o := New()
		o.MechanismName = names.MechanismRR
		o.OutputFormat = "prometheus"
		ok, err := o.Validate()
		require.False(t, ok)
		require.ErrorIs(t, err, ErrOutputFormatOnlyForTSM)
	})

	t.Run("rr without output format", func(t *testing.T) {
		o := New()
		o.MechanismName = names.MechanismRR
		ok, err := o.Validate()
		require.True(t, ok)
		require.NoError(t, err)
	})
}

func TestValidatePool(t *testing.T) {
	o := New()
	o.Pool = Members("prom1", "prom2")
	err := o.ValidatePool("alb1", sets.New([]string{"prom1", "prom2", "prom3"}))
	require.NoError(t, err)

	err = o.ValidatePool("alb1", sets.New([]string{"prom1"}))
	require.Error(t, err)
	require.Contains(t, err.Error(), "prom2")
	require.Contains(t, err.Error(), "alb1")
}

func TestUnmarshalYAMLError(t *testing.T) {
	o := New()
	// a sequence cannot be decoded into the options struct
	err := yaml.Unmarshal([]byte("- boom"), o)
	require.Error(t, err)
}
