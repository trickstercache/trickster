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

package testing

import "testing"

func TestNewTestServer(t *testing.T) {
	s := NewTestServer(200, "OK", map[string]string{"Expires": "-1"})
	if s == nil {
		t.Errorf("Expected server pointer, got %v", s)
	}
}

func TestNewTestWebClient(t *testing.T) {
	s := NewTestWebClient()
	if s == nil {
		t.Errorf("Expected webclient pointer, got %v", s)
	}
}
