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

package clickhouse

import (
	"testing"
)

func TestGetQueryPartsFailure(t *testing.T) {
	query := "this should fail to parse"
	_, _, _, err := getQueryParts(query, "")
	if err == nil {
		t.Errorf("should have produced error")
	}

}

func TestParseQueryExtents(t *testing.T) {

	_, _, err := parseQueryExtents("", map[string]string{})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not find operator`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "", "ts1": "a"})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not find start time`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "between", "ts1": "1", "ts2": "a"})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not determine end time`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "between", "ts1": "1"})
	if err == nil {
		t.Errorf("expected error: %s", `failed to parse query: could not find end time`)
	}

	_, _, err = parseQueryExtents("", map[string]string{"operator": "x", "ts1": "1"})
	if err != nil {
		t.Error(err)
	}

}
