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

package mech_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
)

func TestRecoverFanoutPanicIncrementsMetric(t *testing.T) {
	before := testutil.ToFloat64(metrics.ALBFanoutFailures.WithLabelValues("metric-test", "", "panic"))

	func() {
		defer mech.RecoverFanoutPanic("metric-test", "", 0, nil)
		panic("forced")
	}()

	after := testutil.ToFloat64(metrics.ALBFanoutFailures.WithLabelValues("metric-test", "", "panic"))
	if after-before != 1 {
		t.Errorf("expected counter to increment by 1, before=%v after=%v", before, after)
	}
}

func TestRecoverFanoutPanicVariantLabel(t *testing.T) {
	before := testutil.ToFloat64(metrics.ALBFanoutFailures.WithLabelValues("metric-test", "avg-sum", "panic"))

	func() {
		defer mech.RecoverFanoutPanic("metric-test", "avg-sum", 0, nil)
		panic("forced")
	}()

	after := testutil.ToFloat64(metrics.ALBFanoutFailures.WithLabelValues("metric-test", "avg-sum", "panic"))
	if after-before != 1 {
		t.Errorf("expected variant-labeled counter to increment by 1, before=%v after=%v", before, after)
	}
}

func TestRecoverFanoutPanicNoPanicNoIncrement(t *testing.T) {
	before := testutil.ToFloat64(metrics.ALBFanoutFailures.WithLabelValues("metric-test-noop", "", "panic"))

	func() {
		defer mech.RecoverFanoutPanic("metric-test-noop", "", 0, nil)
		// no panic
	}()

	after := testutil.ToFloat64(metrics.ALBFanoutFailures.WithLabelValues("metric-test-noop", "", "panic"))
	if after != before {
		t.Errorf("expected counter unchanged, before=%v after=%v", before, after)
	}
}
