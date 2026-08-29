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

package server

import "time"

func temporalValue(v any, location *time.Location) (time.Time, bool) {
	if t, ok := v.(time.Time); ok {
		return t, true
	}
	if text, ok := v.(string); ok {
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", time.RFC3339Nano} {
			if t, err := time.ParseInLocation(layout, text, location); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}
