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

package poller

import (
	"testing"

	"go.uber.org/goleak"
)

// goleak guards the poller's central promise: every loop goroutine exits on
// Stop or context cancellation. A regression here would not fail any
// assertion -- it would just leak a goroutine per poller until process exit,
// which is exactly the failure mode the poller exists to prevent in its
// callers. -race will not catch it; only this will.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
