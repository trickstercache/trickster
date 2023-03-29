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
	expected := time.Duration(1) * time.Hour
	d, err := ParseDuration("1h")
	if err != nil {
		t.Error(err)
	}
	if d != expected {
		t.Errorf("expected %d got %d", expected, d)
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
