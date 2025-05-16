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
	"net/http"
	"testing"
)

func TestNextRoundRobin(t *testing.T) {

	p := &pool{}
	p.healthy.Store(&[]http.Handler{http.NotFoundHandler()})

	p2 := nextRoundRobin(p)
	if len(p2) != 1 {
		t.Errorf("expected %d got %d", 1, len(p2))
	}

	p = &pool{}
	p2 = nextRoundRobin(p)
	if len(p2) != 0 {
		t.Errorf("expected %d got %d", 0, len(p2))
	}

}
