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

package cred

import (
	"github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/types"
	"golang.org/x/crypto/bcrypt"
)

// ProcessRawPassword converts the input from plaintext or other format to the
// encryption format used by the Authenticator
func ProcessRawCredential(input string, cf types.CredentialsFormat) (string, error) {
	if cf == types.PlainText || cf == types.Unknown {
		hash, err := bcrypt.GenerateFromPassword([]byte(input), bcrypt.DefaultCost)
		if err != nil {
			return "", err
		}
		return string(hash), nil
	}
	return input, nil
}
