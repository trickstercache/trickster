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

package request

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

func TestGetAndSetBody(t *testing.T) {

	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)

	body := GetBody(req)
	if string(body) != "" {
		t.Errorf("expected `` got `%s`", string(body))
	}

	req.Body = io.NopCloser(bytes.NewReader([]byte("trickster")))
	body = GetBody(req)
	if string(body) != "trickster" {
		t.Errorf("expected `` got `%s`", string(body))
	}

	req = SetBody(req, nil)
	body = GetBody(req)
	if string(body) != "" {
		t.Errorf("expected `` got `%s`", string(body))
	}

	req = SetBody(req, []byte("trickster"))
	body = GetBody(req)
	if string(body) != "trickster" {
		t.Errorf("expected `trickster` got `%s`", string(body))
	}

}
