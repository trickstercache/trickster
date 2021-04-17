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

package timeseries

import "testing"

func TestNewModeler(t *testing.T) {

	f := func([]byte, *TimeRangeQuery) (Timeseries, error) {
		return nil, nil
	}

	m := NewModeler(f, nil, nil, nil, f, nil)

	if m.WireUnmarshaler == nil {
		t.Error("expected non-nil WireUnmarshaler")
	}

	if m.CacheUnmarshaler == nil {
		t.Error("expected non-nil CacheUnmarshaler")
	}

}
