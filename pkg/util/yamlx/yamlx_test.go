/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package yamlx

import "testing"

const testYML = `
frontend:
  test:
    apples: 4
    subkey:
      types:
        - - green
          - red
  : # will not validate this
`

func TestGetKeyList(t *testing.T) {

	keys, err := GetKeyList(testYML)
	if err != nil {
		t.Error(err)
	}

	if _, ok := keys["frontend.test.subkey.types"]; !ok {
		t.Error("missing key")
	}

}
