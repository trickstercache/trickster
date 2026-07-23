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

package promql

import "testing"

func TestParsePromQLScalarLiteralNumberSyntax(t *testing.T) {
	tests := []struct {
		literal string
		want    float64
		valid   bool
	}{
		{literal: "0x2", want: 2, valid: true},
		{literal: "+0Xf", want: 15, valid: true},
		{literal: "-0x_1", want: -1, valid: true},
		{literal: "012", want: 10, valid: true},
		{literal: "0_12", want: 10, valid: true},
		{literal: "09", want: 9, valid: true},
		{literal: "1_000e-3", want: 1, valid: true},
		{literal: "1__0"},
		{literal: "0x1p2"},
		{literal: "0X1.8p1"},
		{literal: "0b10"},
		{literal: "0o10"},
		{literal: "1_0m"},
	}

	for _, tt := range tests {
		t.Run(tt.literal, func(t *testing.T) {
			got, valid := parsePromQLScalarLiteral(tt.literal)
			if valid != tt.valid || valid && got != tt.want {
				t.Fatalf("got value=%v valid=%t want value=%v valid=%t",
					got, valid, tt.want, tt.valid)
			}
		})
	}
}
