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

package alb

import "testing"

func TestNewResponderClaim(t *testing.T) {

	rc := newResponderClaim(1)
	if len(rc.contexts) != 1 {
		t.Error("expected 1 got ", len(rc.contexts))
	}
	if rc.lockVal != -1 {
		t.Error("expected -1 got ", rc.lockVal)
	}

}

func TestClaim(t *testing.T) {

	rc := newResponderClaim(2)

	b := rc.Claim(1)
	if !b {
		t.Error("expected true")
	}

	b = rc.Claim(1)
	if !b {
		t.Error("expected true")
	}

	b = rc.Claim(0)
	if b {
		t.Error("expected false")
	}

}
