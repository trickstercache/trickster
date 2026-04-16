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

package index

import (
	"bytes"
	"testing"
)

func TestObjectRoundTrip(t *testing.T) {
	v := Object{
		Key:   "test-key",
		Size:  1024,
		Value: []byte("cached-data"),
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 Object
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Key != "test-key" {
		t.Fatal("Key mismatch")
	}
	if v2.Size != 1024 {
		t.Fatal("Size mismatch")
	}
	if !bytes.Equal(v2.Value, []byte("cached-data")) {
		t.Fatal("Value mismatch")
	}
}
