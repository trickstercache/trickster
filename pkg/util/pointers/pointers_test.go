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

package pointers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	const port = 8480
	intPtr := New(port)
	if intPtr == nil {
		t.Fatal("expected non-nil")
	}
	if *intPtr != port {
		t.Fatalf("expected %d got %d", port, *intPtr)
	}
	intPtr = Clone(intPtr)
	if intPtr == nil {
		t.Fatal("expected non-nil")
	}
	if *intPtr != port {
		t.Fatalf("expected %d got %d", port, *intPtr)
	}
	intPtr = nil
	intPtr = Clone(intPtr)
	if intPtr != nil {
		t.Fatal("expected nil")
	}
}

func TestClone(t *testing.T) {
	type example struct {
		Name    string
		Details map[string]string
	}
	e := &example{
		Name: "test",
		Details: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
	}
	// clone example
	e2 := Clone(e)
	require.NotNil(t, e2)
	// modify original, verify clone is partially affected
	e.Name = "modified"
	e.Details["key1"] = "modifiedValue1"

	require.Equal(t, "test", e2.Name, "expect simple fields to be safe from modification")
	require.NotEqual(t, "value1", e2.Details["key1"], "expect pointer-based fields to be shallow copied, modification affects clone")
	require.Equal(t, "modifiedValue1", e2.Details["key1"])
}
