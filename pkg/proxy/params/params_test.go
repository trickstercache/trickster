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

package params

import (
	"fmt"
	"net/url"
	"reflect"
	"testing"
)

func TestUpdateParams(t *testing.T) {

	params := url.Values{"param1": {"value1"}, "param3": {"value3"}, "param4": {"value4"}}
	updates := map[string]string{"param2": "value2", "+param3": "value3.1", "-param4": "", "": "empty_key_ignored"}
	expected := url.Values{"param1": {"value1"}, "param2": {"value2"}, "param3": {"value3", "value3.1"}}

	UpdateParams(params, nil)
	if len(params) != 3 {
		t.Errorf("expected %d got %d", 1, len(params))
	}

	UpdateParams(params, map[string]string{})
	if len(params) != 3 {
		t.Errorf("expected %d got %d", 1, len(params))
	}

	UpdateParams(params, updates)
	if !reflect.DeepEqual(params, expected) {
		fmt.Printf("mismatch\nexpected: %v\n     got: %v\n", expected, params)
	}

}
