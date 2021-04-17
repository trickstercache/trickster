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

package providers

import (
	"testing"
)

func TestString(t *testing.T) {

	t1 := None
	t2 := Zipkin
	var t3 Provider = 13

	if t1.String() != "none" {
		t.Errorf("expected %s got %s", "none", t1.String())
	}

	if t2.String() != "zipkin" {
		t.Errorf("expected %s got %s", "zipkin", t2.String())
	}

	if t3.String() != "13" {
		t.Errorf("expected %s got %s", "13", t3.String())
	}

}
