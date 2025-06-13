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

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

func (p *pool) checkHealth() {
	for {
		select {
		case <-p.ctx.Done():
			logger.Debug("stopping ALB pool", nil)
			return
		case <-p.ch: // msg arrives whenever the healthy list must be rebuilt
			if p.hcInProgress.Load() {
				// this skips a health check cycle if one is already in progress
				// to avoid pileups if a target is very slow to respond
				return
			}
			p.hcInProgress.Store(true)
			h := make([]http.Handler, len(p.targets))
			var k int
			for _, t := range p.targets {
				if t.hcStatus.Get() >= p.healthyFloor {
					h[k] = t.handler
					k++
				}
			}
			h = h[:k]
			p.healthy.Store(&h)
			p.hcInProgress.Store(false)
		}
	}
}
