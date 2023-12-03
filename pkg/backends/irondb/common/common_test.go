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

package common

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/util/timeconv"
)

func TestFormatTimestamp(t *testing.T) {
	tm := time.Unix(123456789, int64(time.Millisecond))
	exp := "123456789.001"
	res := FormatTimestamp(tm, true)
	if res != exp {
		t.Errorf("Expected string: %v, got: %v", exp, res)
	}

	tm = time.Unix(123456789, int64(time.Millisecond))
	exp = "123456789"
	res = FormatTimestamp(tm, false)
	if res != exp {
		t.Errorf("Expected string: %v, got: %v", exp, res)
	}
}

func TestParseTimestamp(t *testing.T) {
	v := "123456789.001"
	res, err := ParseTimestamp(v)
	if err != nil {
		t.Fatalf("Error parsing %s: %v", v, err.Error())
	}

	exp := time.Unix(123456789, int64(time.Millisecond))
	if !res.Equal(exp) {
		t.Errorf("Expected time: %v, got: %v", exp, res)
	}

	v = "1.a"
	_, err = ParseTimestamp(v)
	if err == nil {
		t.Fatalf("expected error: %s", "parse timestamp")
	}

}

func TestParseDuration(t *testing.T) {
	sd := "10"
	d, err := ParseDuration(sd)
	if err != nil {
		t.Error(err)
	} else if d != 10*timeconv.Second {
		t.Errorf("expected duration %s, got %s", (10 * timeconv.Second).String(), d.String())
	}

	sd = "10x"
	_, err = ParseDuration(sd)
	if err == nil {
		t.Errorf("expected ParseDuration error")
	}
}
