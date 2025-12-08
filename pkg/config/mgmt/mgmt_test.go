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

package mgmt

import "testing"

func TestValidate(t *testing.T) {
	c := New()
	c.PprofServer = ""

	err := c.Validate()
	if err != nil {
		t.Error(err)
	}

	if c.PprofServer != DefaultPprofServerName {
		t.Errorf("expected %s got %s", DefaultPprofServerName, c.PprofServer)
	}

	c.PprofServer = "x"

	err = c.Validate()
	if err == nil {
		t.Error("expected error for invalid pprof server name")
	}
}
