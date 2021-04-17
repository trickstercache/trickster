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

package forwarding

import (
	"testing"
)

func TestCollapsedForwardingTypeString(t *testing.T) {

	t1 := CFTypeBasic
	t2 := CFTypeProgressive
	var t3 CollapsedForwardingType = 13

	if t1.String() != "basic" {
		t.Errorf("expected %s got %s", "basic", t1.String())
	}

	if t2.String() != "progressive" {
		t.Errorf("expected %s got %s", "progressive", t2.String())
	}

	if t3.String() != "13" {
		t.Errorf("expected %s got %s", "13", t3.String())
	}

	t3 = GetCollapsedForwardingType("basic")
	if t3 != CFTypeBasic {
		t.Errorf("expected %s got %s", "basic", t3.String())
	}

	t3 = GetCollapsedForwardingType("13")
	if t3 != CFTypeBasic {
		t.Errorf("expected %s got %s", "basic", t3.String())
	}

}
