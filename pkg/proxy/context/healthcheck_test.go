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

func TestHealthcheck(t *testing.T) {

	b := HealthCheckFlag(context.TODO())
	if b {
		t.Error("expected false")
	}

	ctx := context.Background()

	b = HealthCheckFlag(ctx)
	if b {
		t.Error("expected false")
	}

	// cover nil short circuit case
	ctx = WithHealthCheckFlag(ctx, true)
	b = HealthCheckFlag(ctx)
	if !b {
		t.Error("expected true")
	}

}
