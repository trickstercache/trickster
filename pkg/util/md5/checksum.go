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

// Package md5 provides string-based checksum functionality
package md5

import (
	"crypto/md5"
	"fmt"
)

// Checksum returns the calculated hex string version of the md5 checksum for the input string
func Checksum(input string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(input)))
}
