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

package timeconv

import (
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestIsIntAtPost(t *testing.T) {
	si := "1"
	v, is, inc := isIntAtPos(si, 0)
	if v != 1 || !is || inc != 1 {
		t.Errorf("expected 1, true, 1, got %d, %t, %d", v, is, inc)
	}
	si = "12345"
	v, is, inc = isIntAtPos(si, 0)
	if v != 12345 || !is || inc != 5 {
		t.Errorf("expected 12345, true, 5, got %d, %t, %d", v, is, inc)
	}
	si = "h"
	v, is, inc = isIntAtPos(si, 0)
	if v != 0 || is || inc != 1 {
		t.Errorf("expected 0, false, 1, got %d, %t, %d", v, is, inc)
	}
}

func TestParseDuration(t *testing.T) {
	tests := map[string]time.Duration{
		"0":      0,
		"1.5s":   1500 * time.Millisecond,
		"-1h30m": -90 * time.Minute,
		"1d2h":   26 * time.Hour,
		"2w3d":   17 * 24 * time.Hour,
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			d, err := ParseDuration(input)
			if err != nil {
				t.Fatal(err)
			}
			if d != expected {
				t.Errorf("expected %s got %s", expected, d)
			}
		})
	}
}

func TestParseDurationOverflow(t *testing.T) {
	tests := []string{
		"9223372036854775808ns",
		"9223372036854775807ns1ns",
		"9223372036854775807d",
		"-9223372036854775808d",
		"-9223372036854775808ns-1ns",
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			if d, err := ParseDuration(input); err == nil {
				t.Errorf("expected overflow error, got %s", d)
			}
		})
	}
}

func TestParseDurationDecimalFailed(t *testing.T) {
	val := "1.2341"
	_, err := ParseDuration(val)
	if err == nil {
		t.Errorf("expected 'unable to parse duration: %s' error", val)
	}
}

func TestParseDurationFailed(t *testing.T) {
	val := "1x"
	d, err := ParseDuration(val)
	if err == nil {
		t.Errorf("expected error, got %s", d.String())
	} else if err.Error() != "duration literal 1x: expected valid duration unit at position 1" {
		t.Errorf("incorrect error message; got %s", err.Error())
	}
	val = "x"
	d, err = ParseDuration(val)
	if err == nil {
		t.Errorf("expected error, got %s", d.String())
	} else if err.Error() != "duration literal x: expected value of at least length 2 at position 0" {
		t.Errorf("incorrect error message; got %s", err.Error())
	}
	val = "1dh"
	d, err = ParseDuration(val)
	if err == nil {
		t.Errorf("expected error, got %s", d.String())
	} else if err.Error() != "duration literal 1dh: expected valid integer value at position 2" {
		t.Errorf("incorrect error message; got %s", err.Error())
	}
	val = "1d10"
	d, err = ParseDuration(val)
	if err == nil {
		t.Errorf("expected error, got %s", d.String())
	} else if err.Error() != "duration literal 1d10: expected valid duration unit at position 4" {
		t.Errorf("incorrect error message; got %s", err.Error())
	}
	val = "1000"
	d, err = ParseDuration(val)
	if err == nil {
		t.Errorf("expected error, got %s", d.String())
	} else if err.Error() != "duration literal 1000: expected valid duration string at position 0" {
		t.Errorf("incorrect error message; got %s", err.Error())
	}
}

func TestDurationYAML(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		output   string
	}{
		{input: "1.5s", expected: 1500 * time.Millisecond, output: "1.5s"},
		{input: "14d", expected: 14 * 24 * time.Hour, output: "336h0m0s"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			value := struct {
				Timeout Duration `yaml:"timeout"`
			}{}
			if err := yaml.Unmarshal([]byte("timeout: "+test.input+"\n"), &value); err != nil {
				t.Fatal(err)
			}
			if time.Duration(value.Timeout) != test.expected {
				t.Errorf("expected %s, got %s", test.expected, time.Duration(value.Timeout))
			}
			out, err := yaml.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			expectedOutput := "timeout: " + test.output + "\n"
			if string(out) != expectedOutput {
				t.Errorf("expected %q, got %q", expectedOutput, out)
			}
		})
	}
}
