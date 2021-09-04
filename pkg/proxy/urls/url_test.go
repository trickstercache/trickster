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

package urls

import (
	"net/http"
	"net/url"
	"testing"
)

func TestClone(t *testing.T) {

	u1, _ := url.Parse("http://user:pass@127.0.0.1:8080/path?param1=param2")
	u2 := Clone(u1)
	if u2.Hostname() != "127.0.0.1" {
		t.Errorf("expected %s got %s", "127.0.0.1", u2.Hostname())
	}

	u1, _ = url.Parse("http://user@127.0.0.1:8080/path?param1=param2")
	u2 = Clone(u1)
	if u2.Hostname() != "127.0.0.1" {
		t.Errorf("expected %s got %s", "127.0.0.1", u2.Hostname())
	}

}

func TestBuildUpstreamURL(t *testing.T) {
	u1, _ := url.Parse("http://127.0.0.1:8080/base-path")
	r, _ := http.NewRequest("GET", "http://test/new-path", nil)
	expected := "/base-path/new-path"
	u2 := BuildUpstreamURL(r, u1)
	if u2.Path != expected {
		t.Errorf("expected %s got %s", expected, u2.Path)
	}
}

func TestSize(t *testing.T) {

	const expected = 24
	u, _ := url.Parse("https://trickstercache.org")
	i := Size(u)
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
	i = Size(nil)
	if i != 0 {
		t.Errorf("expected %d got %d", 0, i)
	}

}
