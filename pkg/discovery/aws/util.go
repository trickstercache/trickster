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

package aws

import (
	"encoding/xml"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// sortedKeys returns a map's keys in a deterministic order.
func sortedKeys(m map[string][]string) []string {
	return slices.Sorted(maps.Keys(m))
}

// xmlUnmarshal is a seam so apiError can be tested without a network.
var xmlUnmarshal = xml.Unmarshal

// maxSummarized bounds how many excluded instances are named in one log
// line, so a wholesale mis-tagging does not emit a megabyte of log.
const maxSummarized = 10

// summarize renders excluded instances as a stable, bounded string. It is
// stable so that repeated identical exclusions can be suppressed rather
// than logged every poll forever.
func summarize(skipped []excluded) string {
	sorted := slices.Clone(skipped)
	slices.SortFunc(sorted, func(a, b excluded) int {
		if c := strings.Compare(a.instanceID, b.instanceID); c != 0 {
			return c
		}
		return strings.Compare(a.reason, b.reason)
	})
	var sb strings.Builder
	for i, e := range sorted {
		if i == maxSummarized {
			sb.WriteString("; and ")
			sb.WriteString(strconv.Itoa(len(sorted) - i))
			sb.WriteString(" more")
			break
		}
		if i > 0 {
			sb.WriteString("; ")
		}
		sb.WriteString(e.instanceID)
		sb.WriteString(": ")
		sb.WriteString(e.reason)
	}
	return sb.String()
}
