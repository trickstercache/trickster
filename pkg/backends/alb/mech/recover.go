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

package mech

import (
	"runtime/debug"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

// RecoverFanoutPanic recovers from a panic in an ALB fanout goroutine and
// invokes onPanic so the caller can mark its per-member result as failed.
// errgroup.Group.Go does not recover panics on its own, so without this a
// single bad upstream handler would crash the whole proxy. variant carries
// optional sub-fanout context (e.g. "avg-sum" / "avg-count" for TSM's
// paired weighted-avg queries); pass "" when the mechanism has only one
// fanout path.
func RecoverFanoutPanic(mech, variant string, member int, onPanic func()) {
	rec := recover()
	if rec == nil {
		return
	}
	logger.Error("alb fanout member panic", logging.Pairs{
		"mech":    mech,
		"variant": variant,
		"member":  member,
		"panic":   rec,
		"stack":   string(debug.Stack()),
	})
	metrics.ALBFanoutFailures.WithLabelValues(mech, variant, "panic").Inc()
	if onPanic != nil {
		onPanic()
	}
}
