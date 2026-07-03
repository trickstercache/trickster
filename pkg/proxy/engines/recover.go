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

package engines

import (
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/util/safego"
)

// goWithRecover runs fn in a new goroutine guarded by defer-recover. A panic
// inside any of the proxy/engines fire-and-forget goroutines (DPC cache
// invalidation, upstream access-log emission, PCF io.Copy pumps) would
// otherwise crash the trickster process, since the goroutine has no caller
// frame above it to absorb the panic. The site label is recorded on the
// ProxyEnginesPanicRecovered counter so operators can localize the failure.
func goWithRecover(site string, fn func()) {
	safego.Go(func(r any, stack []byte) {
		logger.Error("proxy engines goroutine panic", logging.Pairs{
			"site":  site,
			"panic": r,
			"stack": string(stack),
		})
		metrics.ProxyEnginesPanicRecovered.WithLabelValues(site).Inc()
	}, fn)
}
