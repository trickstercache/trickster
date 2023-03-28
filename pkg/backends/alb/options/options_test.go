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
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

type testObj struct {
	Options *Options
}

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
	o.FgrCodesLookup = map[int]interface{}{200: "test"}
	if o == nil {
		t.Error("expected non-nil")
	}
	co := o.Clone()

	if len(co.Pool) != 1 || co.Pool[0] != "test" {
		t.Error("clone mismatch")
	}
	if len(co.FGRStatusCodes) != 1 || co.FGRStatusCodes[0] != 200 {
		t.Error("status codes mismatch")
	}
	if len(co.FgrCodesLookup) != 1 || co.FgrCodesLookup[200] != "test" {
		t.Error("fgr lookup mismatch")
	}
}

func TestSetDefaults(t *testing.T) {

	o2, err := SetDefaults("test", nil, nil)
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
	_, err = SetDefaults("test", o, md)
	if err != nil {
		t.Error(err)
	}

	o, md, err = fromYAML(testTOMLNoALB)
	if err != nil {
		t.Error(err)
	}
	o2, err = SetDefaults("test", o, md)
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
	_, err = SetDefaults("test", o, md)
	if err == nil {
		t.Error("expected output_format error")
	}

	o, md, err = fromYAML(testTOMLBadOutputFormat2)
	if err != nil {
		t.Error(err)
	}
	_, err = SetDefaults("test", o, md)
	if err == nil {
		t.Error("expected output_format error")
	}

	o, md, err = fromYAML(testFGR)
	if err != nil {
		t.Error(err)
	}
	_, err = SetDefaults("test", o, md)
	if err != nil {
		t.Error("failed to set defaults")
	}

	_, md, err = fromYAML(testFGR)
	if err != nil {
		t.Error(err)
	}
	_, err = SetDefaults("test", o, md)
	if err != nil {
		t.Error("failed to set defaults")
	}

}
