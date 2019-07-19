/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestCopy(t *testing.T) {
	c1 := NewConfig()
	c2 := c1.copy()
	if !reflect.DeepEqual(c1, c2) {
		t.Errorf("copy mistmatch")
	}
}

func TestString(t *testing.T) {
	c1 := NewConfig()
	s := c1.String()
	if strings.Index(s, `password = "*****"`) < 0 {
		t.Errorf("missing password mask: %s", "*****")
	}
}