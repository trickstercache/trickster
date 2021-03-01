/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

	"github.com/BurntSushi/toml"
)

type testObj struct {
	Options *Options
}

type testOptions1 struct {
	Backends map[string]*testOptions2 `toml:"backends"`
}

type testOptions2 struct {
	Alb *Options `toml:"alb"`
}

func fromTOML(conf string) (*Options, *toml.MetaData, error) {

	to := &testOptions1{}
	md, err := toml.Decode(conf, to)
	if err != nil {
		return nil, nil, err
	}

	for _, v := range to.Backends {
		if v.Alb != nil {
			return v.Alb, &md, nil
		}
	}
	return nil, &md, nil
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
	o.MergeablePaths = []string{"/test"}
	if o == nil {
		t.Error("expected non-nil")
	}
	co := o.Clone()

	if len(co.Pool) != 1 || co.Pool[0] != "test" {
		t.Error("clone mismatch")
	}

	if len(co.MergeablePaths) != 1 || co.MergeablePaths[0] != "/test" {
		t.Error("clone mismatch")
	}

}

func TestProcessTOML(t *testing.T) {

	o2, err := ProcessTOML("test", nil, nil)
	if err != nil {
		t.Error(err)
	}
	if o2 != nil {
		t.Error("expected nil Options")
	}

	o, md, err := fromTOML(testTOML)
	if err != nil {
		t.Error(err)
	}
	_, err = ProcessTOML("test", o, md)
	if err != nil {
		t.Error(err)
	}

	o, md, err = fromTOML(testTOMLNoALB)
	if err != nil {
		t.Error(err)
	}
	o2, err = ProcessTOML("test", o, md)
	if err != nil {
		t.Error(err)
	}
	if o2 != nil {
		t.Error("expected nil Options")
	}

	o, md, err = fromTOML(testTOMLBadOutputFormat1)
	if err != nil {
		t.Error(err)
	}
	_, err = ProcessTOML("test", o, md)
	if err == nil {
		t.Error("expected output_format error")
	}

	o, md, err = fromTOML(testTOMLBadOutputFormat2)
	if err != nil {
		t.Error(err)
	}
	_, err = ProcessTOML("test", o, md)
	if err == nil {
		t.Error("expected output_format error")
	}

}
