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

package httpsd

import (
	"fmt"
	"os"
	"strings"
)

// readTokenFile reads a bearer token from disk, trimming trailing
// whitespace. It is read per poll rather than cached: the file is the
// rotation mechanism (a projected service-account token, a Vault-issued
// credential), so caching it would defeat the reason for choosing the file
// form over the literal one.
func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- operator-configured path
	if err != nil {
		return "", fmt.Errorf("reading bearer_token_file: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", fmt.Errorf("bearer_token_file %q is empty", path)
	}
	return token, nil
}
