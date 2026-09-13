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

package context

import (
	"context"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
)

func TestHops(t *testing.T) {
	ctx := context.Background()
	_, j := Hops(ctx)
	if j != options.DefaultMaxRuleExecutions {
		t.Errorf("expected %d got %d", options.DefaultMaxRuleExecutions, j)
	}

	ctx = WithHops(ctx, 0, 1)
	i, j := Hops(ctx)

	if i != 0 {
		t.Errorf("expected %d got %d", 0, i)
	}

	if j != 1 {
		t.Errorf("expected %d got %d", 1, j)
	}

	ctx = context.Background()
	IncrementedRewriterHops(ctx, 5)
	_ = RewriterHops(ctx)
	ctx = StartRewriterHops(ctx)
	IncrementedRewriterHops(ctx, 5)
	i = RewriterHops(ctx)
	if i != 5 {
		t.Error("expected 5 got", i)
	}
}

func TestHopsIfSet(t *testing.T) {
	current, maxHops, ok := HopsIfSet(context.Background())
	if ok || current != 0 || maxHops != options.DefaultMaxRuleExecutions {
		t.Errorf("expected defaults and unset, got %d %d %t", current, maxHops, ok)
	}
	current, maxHops, ok = HopsIfSet(WithHops(context.Background(), 3, 40))
	if !ok || current != 3 || maxHops != 40 {
		t.Errorf("expected 3 40 set, got %d %d %t", current, maxHops, ok)
	}
}
