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

package rule

import (
	"github.com/trickstercache/trickster/v2/pkg/encoding/base64"
)

type encoding string
type decodingFunc func(string, string, int) string

var decodingFuncs = map[encoding]decodingFunc{
	"base64": decodeBase64,
}

func decodeBase64(input, unusedS string, unusedI int) string {
	if input == "" {
		return ""
	}
	s, err := base64.Decode(input)
	if err != nil {
		return ""
	}
	return s
}
