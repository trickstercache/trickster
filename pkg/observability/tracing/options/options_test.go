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
)

func TestNew(t *testing.T) {
	o := New()
	o.Endpoint = "test:1234"
	o2 := o.Clone()
	if o2.Endpoint != "test:1234" {
		t.Error("clone failed")
	}
}

func TestProcessTracingOptions(t *testing.T) {
	ProcessTracingOptions(nil)
	o := New()
	mo := Lookup{
		"test": o,
	}
	ProcessTracingOptions(mo)
	if o.SampleRate == nil {
		t.Error("expected SampleRate to be set")
	} else if int(*o.SampleRate) != 1 {
		t.Errorf("expected 1 got %d", int(*o.SampleRate))
	}

	o2 := New()
	sampleRate := 2.0
	o2.SampleRate = &sampleRate
	mo2 := Lookup{
		"test2": o2,
	}
	ProcessTracingOptions(mo2)
	if o2.SampleRate == nil {
		t.Error("expected SampleRate to be set")
	} else if int(*o2.SampleRate) != 1 {
		t.Errorf("expected 1 got %d", int(*o2.SampleRate))
	}

	o3 := New()
	sampleRate3 := 0.0
	o3.SampleRate = &sampleRate3
	mo3 := Lookup{
		"test3": o3,
	}
	ProcessTracingOptions(mo3)
	if o3.SampleRate == nil {
		t.Error("expected SampleRate to be set")
	} else if int(*o3.SampleRate) != 0 {
		t.Errorf("expected 0 got %d", int(*o3.SampleRate))
	}
}

func TestGenerateOmitTags(t *testing.T) {

	o := &Options{OmitTagsList: []string{"test1"}}
	o.generateOmitTags()
	if _, ok := o.OmitTags["test1"]; !ok {
		t.Error("expected map entry")
	}
}

func TestAttachTagsToSpan(t *testing.T) {

	o := &Options{Provider: "zipkin", Tags: map[string]string{"test": "test"}}
	if o.AttachTagsToSpan() {
		t.Error("expected false")
	}
	o.setAttachTags()
	if !o.AttachTagsToSpan() {
		t.Error("expected true")
	}

}
