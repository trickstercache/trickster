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
	_, err := ParseDuration(val)
	if err == nil {
		t.Errorf("expected 'unable to parse duration: %s' error", val)
	}
}

func TestParseDurationParts(t *testing.T) {
	expected := time.Duration(1) * time.Hour
	d, err := ParseDurationParts(1, "h")
	if err != nil {
		t.Error(err)
	}
	if d != expected {
		t.Errorf("expected %d got %d", expected, d)
	}
}

func TestParseDurationPartsFailed(t *testing.T) {
	_, err := ParseDurationParts(1, "x")
	if err == nil {
		t.Errorf("expected 'unable to parse duration 1x' error")
	}
}
