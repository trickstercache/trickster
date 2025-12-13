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
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
)

func (p *pool) checkHealth() {
	for {
		select {
		case <-p.ctx.Done():
			logger.Debug("stopping ALB pool", nil)
			return
		case <-p.ch: // msg arrives whenever the healthy list must be rebuilt
			p.RefreshHealthy()
		}
	}
}
