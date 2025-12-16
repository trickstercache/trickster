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

package albpool

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func New(healthyFloor int, hs []http.Handler) (pool.Pool,
	[]*pool.Target, []*healthcheck.Status,
) {
	var targets []*pool.Target
	var statuses []*healthcheck.Status
	if len(hs) > 0 {
		targets = make([]*pool.Target, 0, len(hs))
		statuses = make([]*healthcheck.Status, 0, len(hs))
		for _, h := range hs {
			hst := &healthcheck.Status{}
			statuses = append(statuses, hst)
			targets = append(targets, pool.NewTarget(h, hst, nil))
		}
	}
	pool := pool.New(targets, healthyFloor)
	return pool, targets, statuses
}
