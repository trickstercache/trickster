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

package pool

import (
	"testing"

	"go.uber.org/goleak"
)

// goleak holds the pool to running no goroutines of its own: health transitions reach it
// synchronously, so a test that leaves one behind has reintroduced a worker.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
