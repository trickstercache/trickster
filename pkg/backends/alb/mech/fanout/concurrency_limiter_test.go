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

package fanout

import (
	"context"
	"errors"
	"testing"
)

func TestConcurrencyLimiter(t *testing.T) {
	if limiter := NewConcurrencyLimiter(0); limiter != nil {
		t.Fatal("zero limit unexpectedly created a limiter")
	}
	if limiter := NewConcurrencyLimiter(-1); limiter != nil {
		t.Fatal("negative limit unexpectedly created a limiter")
	}

	limiter := NewConcurrencyLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := limiter.acquire(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire: got %v want context.Canceled", err)
	}
	limiter.release()
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	limiter.release()
}
