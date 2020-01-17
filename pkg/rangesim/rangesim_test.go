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

package rangesim

import (
	"bytes"
	"testing"
)

func TestParseRangeHeader(t *testing.T) {

	br := parseRangeHeader("bytes=0-10,20-40")
	if len(br) != 2 {
		t.Errorf("expected %d got %d", 2, len(br))
	}

	br = parseRangeHeader("")
	if br != nil {
		t.Errorf("expected nil got %v", br)
	}

	br = parseRangeHeader("bytes=10")
	if br != nil {
		t.Errorf("expected nil got %v", br)
	}

	br = parseRangeHeader("bytes=a0-n0")
	if br != nil {
		t.Errorf("expected nil got %v", br)
	}

}

func TestWriteMultipartResponse(t *testing.T) {

	br := parseRangeHeader("bytes=0-10,20-40")
	buff := make([]byte, 0)
	w := bytes.NewBuffer(buff)

	err := br.writeMultipartResponse(w)
	if err != nil {
		t.Error(err)
	}

}

func TestValidate(t *testing.T) {

	br := parseRangeHeader("bytes=0-10,20-40")
	v := br.validate()
	if !v {
		t.Errorf("expected %t got %t", true, v)
	}

	br[1].start = 45
	v = br.validate()
	if v {
		t.Errorf("expected %t got %t", false, v)
	}

}
