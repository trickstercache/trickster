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
)

func nextRoundRobin(p *pool) []http.Handler {
	p.mtx.RLock()
	t := p.healthy
	p.mtx.RUnlock()
	if len(t) == 0 {
		return nil
	}
	i := p.pos.Add(1) % uint64(len(t))
	return []http.Handler{t[i]}
}
