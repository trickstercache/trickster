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

package context

import (
	"context"
	"testing"
)

type testStruct struct {
	testField1 bool
}

func TestResources(t *testing.T) {

	ctx := context.Background()

	// cover nil short circuit case
	ctx = WithResources(ctx, nil)

	r1 := &testStruct{testField1: true}
	ctx = WithResources(ctx, r1)
	r2 := Resources(ctx)

	if !r2.(*testStruct).testField1 {
		t.Errorf("expected %t got %t", true, r2.(testStruct).testField1)
	}

}
