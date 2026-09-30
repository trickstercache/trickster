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

package index

import (
	"context"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/metrics"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	gm "github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

// a sweep at the start when the index cannot vouch for itself, then one each scan interval
func (idx *IndexedClient) scannerWorker(ctx context.Context) {
	defer idx.wg.Done()
	safego.Run(idx.workerPanicHandler(workerScanner, &idx.scannerExited), func() {
		if idx.sweepDue {
			idx.sweep(ctx)
			signal(idx.hasSwept)
		}
	SCANNER:
		for {
			var interval <-chan time.Time
			if si := idx.options.Load().ScanInterval; si > 0 {
				interval = time.After(time.Duration(si))
			}
			select {
			case <-ctx.Done():
				break SCANNER
			case <-interval:
			case <-idx.forceSweep:
			}
			idx.sweep(ctx)
			signal(idx.hasSwept)
		}
		idx.scannerExited.Store(true)
	})
}

// an object the cache holds and the index did not know of
func (idx *IndexedClient) adopt(m *cache.ObjectMeta, sweep uint64) {
	o, _ := idx.put(m.Key, m.Size, m.LastWrite.UnixNano(), m.LastWrite.UnixNano(), unixNano(m.Expiration), false)
	o.sweep.Store(sweep)
	idx.journal.add(o)
}

// objects the index did not know of are listed; those the cache no longer holds are dropped
func (idx *IndexedClient) sweep(ctx context.Context) {
	sweep := idx.sweeps.Add(1)
	start := time.Now()
	var after string
	var adopted, dropped float64
	for done := false; !done; {
		o := idx.options.Load()
		batch := o.ScanBatchSize
		if batch <= 0 {
			batch = options.DefaultScanBatchSize
		}
		var err error
		after, done, err = idx.scanner.ScanMeta(after, batch, func(m cache.ObjectMeta) {
			if m.Key == IndexKey {
				return
			}
			if v, ok := idx.objects.Load(m.Key); ok {
				v.(*Object).sweep.Store(sweep)
				return
			}
			idx.adopt(&m, sweep)
			adopted++
		})
		if err != nil {
			logger.Warn("cache sweep was not completed",
				logging.Pairs{keys.CacheName: idx.name, keys.Error: err.Error()})
			return
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(o.ScanBatchPause)):
		}
	}
	// an object written since the sweep began may lie behind it, and is left alone
	beganNano := start.UnixNano()
	var lost []string
	idx.each(func(o *Object) bool {
		if o.sweep.Load() != sweep && o.lastWrite.Load() < beganNano {
			lost = append(lost, o.Key)
		}
		return true
	})
	for _, key := range lost {
		if _, gone := idx.forgetUnlessWrittenSince(key, start); gone {
			dropped++
		}
	}
	gm.CacheEvents.WithLabelValues(idx.name, idx.cacheProvider, reasonSweep, reasonAdopted).Add(adopted)
	gm.CacheEvents.WithLabelValues(idx.name, idx.cacheProvider, reasonSweep, reasonDropped).Add(dropped)
	metrics.ObserveCacheEvent(idx.name, idx.cacheProvider, eventIndex, reasonSweep)
	logger.Debug("cache sweep completed", logging.Pairs{
		keys.CacheName: idx.name, reasonAdopted: adopted, reasonDropped: dropped,
		"elapsed": time.Since(start).String(),
	})
}
