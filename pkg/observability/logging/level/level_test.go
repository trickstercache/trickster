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

package level

import (
	"strings"
	"testing"
)

func TestGetID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		level Level
		want  ID
	}{
		{name: Debug, level: Debug, want: DebugID},
		{name: Info, level: Info, want: InfoID},
		{name: Warn, level: Warn, want: WarnID},
		{name: Error, level: Error, want: ErrorID},
		{name: Fatal, level: Fatal, want: TraceID},
		{name: "invalid", level: "invalid", want: 0},
		{name: "empty", level: "", want: 0},
		{name: "uppercase", level: strings.ToUpper(Info), want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := GetID(tc.level); got != tc.want {
				t.Errorf("GetID(%q) = %d, want %d", tc.level, got, tc.want)
			}
		})
	}
}

func BenchmarkGetID(b *testing.B) {
	for b.Loop() {
		GetID(Info)
	}
}
