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

package pgfamily

import (
	"regexp"
	"strings"
)

var boundPlaceholder = regexp.MustCompile(`<\$[A-Za-z0-9_]+\$>`)

// MaskPlaceholders blanks each time-bound placeholder in place, moving no byte offset.
// Its dollar signs would otherwise scan as the start of a dollar-quoted string.
func MaskPlaceholders(sql string) string {
	return boundPlaceholder.ReplaceAllStringFunc(sql, func(token string) string {
		return strings.Repeat("0", len(token))
	})
}
