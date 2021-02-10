/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package backends

import (
	"testing"

	"github.com/gorilla/mux"
	bo "github.com/tricksterproxy/trickster/pkg/backends/options"
)

func TestBackends(t *testing.T) {

	cl, _ := New("test1", bo.New(), nil, mux.NewRouter(), nil)
	o := Backends{"test1": cl}

	c := o.Get("test1")
	if c == nil {
		t.Error("expected non-nil client")
	}

	c = o.Get("invalid")
	if c != nil {
		t.Error("expected nil client")
	}

	cfg := o.GetConfig("test1")
	if cfg == nil {
		t.Error("expected non-nil config")
	}

	cfg = o.GetConfig("invalid")
	if cfg != nil {
		t.Error("expected nil config")
	}

	r := o.GetRouter("test1")
	if r == nil {
		t.Error("expected non-nil router")
	}

	r = o.GetRouter("invalid")
	if r != nil {
		t.Error("expected nil router")
	}
}
