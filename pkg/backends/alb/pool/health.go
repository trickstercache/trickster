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
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

// runWithRecover runs fn under defer-recover. A panic inside a refresh worker
// would otherwise kill the goroutine and leave the healthy-targets snapshot
// stale. The per-call re-filter in Targets() still produces correct dispatch
// even with a dead worker, so this is defense-in-depth for operator
// observability (healthy-count gauges, status-driven dashboards) rather than
// for correctness. Known panic scenarios this guards against: a Target whose
// hcStatus is mutated to nil concurrent with RefreshHealthy, and any future
// subscriber callback added to the status-update path that could panic.
func (p *pool) runWithRecover(worker string, fn func()) {
	safego.Run(func(r any, stack []byte) {
		logger.Error("alb pool refresh worker panic", logging.Pairs{
			"worker": worker,
			"panic":  r,
			"stack":  string(stack),
		})
		metrics.ALBPoolRefreshPanicRecovered.WithLabelValues(worker).Inc()
	}, fn)
}

// listenStatusUpdates bridges target health-status notifications into refresh
// scheduling. It marks the pool list dirty for every received update and
// coalesces worker wakeups via scheduleRefresh so bursty changes cannot strand
// a stale healthy-target list.
func (p *pool) listenStatusUpdates() {
	defer p.workers.Done()
	for {
		stop := false
		p.runWithRecover("listenStatusUpdates", func() {
			select {
			case <-p.done:
				stop = true
				return
			case <-p.statusCh:
				p.scheduleRefresh()
			}
		})
		if stop {
			return
		}
	}
}

func (p *pool) checkHealth() {
	defer p.workers.Done()
	for {
		stop := false
		p.runWithRecover("checkHealth", func() {
			select {
			case <-p.done:
				logger.Debug("stopping ALB pool", nil)
				stop = true
				return
			case <-p.ch: // msg arrives whenever the healthy list must be rebuilt
				// this coalesces bursts of updates into a single refresh
				for p.refreshPending.Swap(false) {
					p.RefreshHealthy()
				}
			}
		})
		if stop {
			return
		}
	}
}
