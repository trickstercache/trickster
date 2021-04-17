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

package encoding

import (
	"strings"
)

// MergeCommaSeparated accepts two strings, both representing a comma-separated
// list of values, which are split into their constituted values and returned
// as a merged, dupe-killed list of values, separated by comma and space. The
// returned order is all unique values in s1, followed by the unique values from
// s2 not present in s1
//
// Example Input: s1="zstd, gzip,deflate, gzip" s2="br,gzip, deflate"
// Example Output: "zstd, gzip, deflate, br"
//
//
func MergeCommaSeparated(s1, s2 string) string {
	return MergeDelimeterSeparated(s1, s2, ",", true)
}

// MergeDelimeterSeparated accepts two strings, both representing a delimiter-
// separated list of values, which are split into their constituted values on
// the delimiter and returned as a merged, dupe-killed list. Whitespace on
// either side of the delimiter are trimmed. This is a simple replacement and
// does not recognize escape sequences, etc. When pad is true, the merged list
// will include a space following each delimiter instance.
func MergeDelimeterSeparated(s1, s2, delimiter string, pad bool) string {
	used := make(map[string]interface{})
	l1 := strings.Split(s1, delimiter)
	l2 := strings.Split(s2, delimiter)
	full := make([]string, 0, len(l1)+len(l2))
	for _, v := range l1 {
		v = strings.Trim(v, " \n\t")
		if _, ok := used[v]; ok {
			continue
		}
		used[v] = nil
		full = append(full, v)
	}
	for _, v := range l2 {
		v = strings.Trim(v, " \n\t")
		if _, ok := used[v]; ok {
			continue
		}
		used[v] = nil
		full = append(full, v)
	}
	if pad {
		delimiter += " "
	}
	return strings.Join(full, delimiter)
}
