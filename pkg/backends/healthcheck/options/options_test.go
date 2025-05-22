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
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil options")
	}
}

func TestSetYAMLData(t *testing.T) {
	o := New()
	o.SetYAMLData(nil)
	if o.y != nil {
		t.Error("expected nil metadata")
	}
}

func TestClone(t *testing.T) {
	o := New()
	o.Verb = "trickster"
	o.ExpectedHeaders = map[string]string{}

	o2 := o.Clone()

	if o2.Verb != "trickster" {
		t.Error("clone mismatch")
	}

}

func TestURL(t *testing.T) {
	o := New()
	o.Scheme = "https"
	o.Host = "trickstercache.org"
	o.Path = "/"
	o.Query = "?somequeryparam=somevalue"

	const expected = "https://trickstercache.org/?somequeryparam=somevalue"

	u := o.URL()
	if u.String() != expected {
		t.Errorf("expected %s got %s", expected, u.String())
	}

}

func TestHasExpectedBody(t *testing.T) {
	o := New()
	o.hasExpectedBody = true
	if !o.HasExpectedBody() {
		t.Error("expected true")
	}
}

func TestSetExpectedBody(t *testing.T) {
	o := New()
	o.SetExpectedBody("trickster")
	if !o.HasExpectedBody() {
		t.Error("expected true")
	}
	if !(o.ExpectedBody == "trickster") {
		t.Errorf("expected %s got %s", "trickster", o.ExpectedBody)
	}
}

func TestOverlay(t *testing.T) {

	o := New()
	o.Overlay("", nil)
	if o.Interval != 0 {
		t.Error("expected 0")
	}

	c := &Options{}
	err := yaml.Unmarshal([]byte(hcTOML), c)
	if err != nil {
		t.Error(err)
	}

	y, err := yamlx.GetKeyList(hcTOML)
	if err != nil {
		t.Error(err)
	}

	o2 := New()
	o2.y = y
	o.Overlay("test", o2)
	if o.Interval != 0 {
		t.Error("expected 5000 got ", o.Interval)
	}
}

const hcTOML = `
backends:
  test:
    healthcheck:
      path: test_path
      verb: POST
      query: '?myqueryparam=myqueryval'
      body: custom body
      expected_codes:
        - 200
      expected_body: expected body
      interval: 0
      headers:
        TestHeader: test-header-val
      expected_headers:
        TestHeader: test-header-val
`

func TestCalibrateTimeout(t *testing.T) {

	const defaultTimeout = DefaultHealthCheckTimeout
	const maxTimeout = MaxProbeWait
	const minTimeout = MinProbeWait

	tests := []struct {
		input    time.Duration
		expected time.Duration
	}{
		{
			-1, defaultTimeout,
		},
		{
			0, defaultTimeout,
		},
		{
			1, minTimeout,
		},
		{
			MinProbeWait, minTimeout,
		},
		{
			MinProbeWait + 5, time.Duration(MinProbeWait + 5),
		},
		{
			MaxProbeWait, maxTimeout,
		},
		{
			MaxProbeWait + 5, maxTimeout,
		},
	}

	for i, test := range tests {
		t.Run(strconv.Itoa(i),
			func(t *testing.T) {
				out := CalibrateTimeout(test.input)
				if out != test.expected {
					t.Errorf("expected %v got %v", test.expected, out)
				}
			})
	}
}
