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

	ae "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

type testOptions1 struct {
	Backends map[string]*testOptions2 `yaml:"backends,omitempty"`
}

type testOptions2 struct {
	Alb *Options `yaml:"alb,omitempty"`
}

func fromYAML(conf string) (*Options, yamlx.KeyLookup, error) {

	to := &testOptions1{}
	err := yaml.Unmarshal([]byte(conf), to)
	if err != nil {
		return nil, nil, err
	}
	md, err := yamlx.GetKeyList(conf)
	if err != nil {
		return nil, nil, err
	}

	for _, v := range to.Backends {
		if v != nil && v.Alb != nil {
			return v.Alb, md, nil
		}
	}
	return nil, md, nil
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

func TestOverlayYAMLData(t *testing.T) {

	_, err := OverlayYAMLData("test", nil, nil)
	if err != ae.ErrInvalidOptionsMetadata {
		t.Error("expected error for invalid options metadata", err)
	}

	o2, err := OverlayYAMLData("test", nil, yamlx.KeyLookup{})
	if err != nil {
		t.Error(err)
	}
	if o2 != nil {
		t.Error("expected nil Options")
	}

	o, md, err := fromYAML(testTOML)
	if err != nil {
		t.Error(err)
	}
	_, err = OverlayYAMLData("test", o, md)
	if err != nil {
		t.Error(err)
	}

	o, md, err = fromYAML(testTOMLNoALB)
	if err != nil {
		t.Error(err)
	}
	o2, err = OverlayYAMLData("test", o, md)
	if err != nil {
		t.Error(err)
	}
	if o2 != nil {
		t.Error("expected nil Options")
	}

	o, md, err = fromYAML(testTOMLBadOutputFormat1)
	if err != nil {
		t.Error(err)
	}
	_, err = OverlayYAMLData("test", o, md)
	if err == nil {
		t.Error("expected output_format error")
	}

	o, md, err = fromYAML(testTOMLBadOutputFormat2)
	if err != nil {
		t.Error(err)
	}
	_, err = OverlayYAMLData("test", o, md)
	if err == nil {
		t.Error("expected output_format error")
	}

	o, md, err = fromYAML(testFGR)
	if err != nil {
		t.Error(err)
	}
	_, err = OverlayYAMLData("test", o, md)
	if err != nil {
		t.Error("failed to set defaults")
	}

	_, md, err = fromYAML(testFGR)
	if err != nil {
		t.Error(err)
	}
	_, err = OverlayYAMLData("test", o, md)
	if err != nil {
		t.Error("failed to set defaults")
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
