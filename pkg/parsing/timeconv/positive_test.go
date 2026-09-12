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
	"errors"
	"testing"
	"time"
)

func TestParsePositiveDuration(t *testing.T) {
	d, err := ParsePositiveDuration("30s")
	if err != nil || d != 30*time.Second {
		t.Fatalf("expected 30s, got %v %v", d, err)
	}
	d, err = ParsePositiveDuration("2d")
	if err != nil || d != 48*time.Hour {
		t.Fatalf("expected 48h, got %v %v", d, err)
	}
	for _, in := range []string{"", "600", "abc", "1x"} {
		if _, err := ParsePositiveDuration(in); err == nil {
			t.Errorf("expected a format error for %q", in)
		} else if errors.Is(err, ErrNonPositiveDuration) {
			t.Errorf("expected a format error for %q, got %v", in, err)
		}
	}
	for _, in := range []string{"0", "0s", "-5s"} {
		if _, err := ParsePositiveDuration(in); !errors.Is(err, ErrNonPositiveDuration) {
			t.Errorf("expected %v for %q, got %v", ErrNonPositiveDuration, in, err)
		}
	}
}
