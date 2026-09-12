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

package headers

import (
	"net/http"
	"testing"
)

func TestConsumeMaxForwards(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		value      string
		unset      bool
		expectStop bool
		expectVal  string
	}{
		{"options zero stops here", http.MethodOptions, "0", false, true, "0"},
		{"trace zero stops here", http.MethodTrace, "0", false, true, "0"},
		{"options five decrements", http.MethodOptions, "5", false, false, "4"},
		{"options one decrements to zero", http.MethodOptions, "1", false, false, "0"},
		{"padded value is decremented", http.MethodOptions, " 3 ", false, false, "2"},
		{"get is not subject to max-forwards", http.MethodGet, "0", false, false, "0"},
		{"absent field forwards unchanged", http.MethodOptions, "", true, false, ""},
		{"empty field forwards unchanged", http.MethodOptions, "", false, false, ""},
		{"unparsable field forwards unchanged", http.MethodOptions, "abc", false, false, "abc"},
		{"negative field forwards unchanged", http.MethodOptions, "-1", false, false, "-1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, err := http.NewRequest(test.method, "http://example.com/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !test.unset {
				r.Header.Set(NameMaxForwards, test.value)
			}
			if got := ConsumeMaxForwards(r); got != test.expectStop {
				t.Errorf("got %t expected %t", got, test.expectStop)
			}
			if got := r.Header.Get(NameMaxForwards); got != test.expectVal {
				t.Errorf("got %q expected %q", got, test.expectVal)
			}
		})
	}
}

func TestConsumeMaxForwardsNilInputs(t *testing.T) {
	if ConsumeMaxForwards(nil) {
		t.Error("expected false")
	}
	if ConsumeMaxForwards(&http.Request{Method: http.MethodOptions}) {
		t.Error("expected false")
	}
}
