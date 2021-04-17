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

package profile

import (
	"context"
	"testing"
)

func TestContext(t *testing.T) {

	ctx := context.Background()
	ep := &Profile{Supported: 8}
	ctx2 := ToContext(ctx, ep)

	ep2 := FromContext(ctx2)
	if ep2.Supported != 8 {
		t.Errorf("expected %d got %d", 8, ep2.Supported)
	}

	ep2 = FromContext(nil)
	if ep2 != nil {
		t.Error("expected nil")
	}

	ep2 = FromContext(context.Background())
	if ep2 != nil {
		t.Error("expected nil")
	}

	ctx2 = ToContext(nil, ep)
	if ctx2 != nil {
		t.Error("expected nil")
	}
}
