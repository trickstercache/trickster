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

package proxy

import (
	"net/http"
	"testing"
)

func TestDocumentFromHTTPResponse(t *testing.T) {

	expected := []byte("1234")

	resp := &http.Response{}
	resp.Header = make(http.Header)
	resp.StatusCode = 200
	d := DocumentFromHTTPResponse(resp, []byte("1234"))

	if string(d.Body) != string(expected) {
		t.Errorf("wanted %s got %s", string(expected), string(d.Body))
	}

	if d.StatusCode != 200 {
		t.Errorf("wanted %d got %d", 200, d.StatusCode)
	}

}
