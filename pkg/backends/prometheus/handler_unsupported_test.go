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

package prometheus

import (
	"net/http/httptest"
	"testing"
)

func TestUnsupportedHandler(t *testing.T) {

	b, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	c := b.(*Client)
	w := httptest.NewRecorder()
	c.UnsupportedHandler(w, nil)

	const expected = `{"status":"error","error":"trickster does not support proxying this endpoint"}`

	if w.Code != 400 {
		t.Errorf("expected %d got %d", 400, w.Code)
	}

	if w.Body.String() != expected {
		t.Errorf("expected %s got %s", expected, w.Body.String())

	}

}
