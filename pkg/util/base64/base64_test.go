/**
* Copyright 2018 The Trickster Authors
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

package base64

import (
	"testing"
)

func TestDecode(t *testing.T) {

	var s string

	_, err := Decode("asd")
	if err == nil {
		t.Error("expected error for invalid input")
	}

	s, err = Decode("dHJpY2tzdGVy")
	if err != nil {
		t.Error(err)
	}

	if s != "trickster" {
		t.Errorf("expected %s got %s", "trickster", s)
	}

}

func TestEncode(t *testing.T) {

	s := Encode("trickster")

	if s != "dHJpY2tzdGVy" {
		t.Errorf("expected %s got %s", "dHJpY2tzdGVy", s)
	}

}
