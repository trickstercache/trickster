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
	"context"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/healthcheck"
)

func TestCheckHealth(t *testing.T) {

	ctx, cancel := context.WithCancel(context.Background())

	tgt := &Target{
		hcStatus: &healthcheck.Status{},
	}

	tgt.hcStatus.Set(1)

	p := &pool{ch: make(chan bool), ctx: ctx, targets: []*Target{tgt}, healthyFloor: -1}
	go func() {
		p.checkHealth()
	}()
	time.Sleep(150 * time.Millisecond)
	p.ch <- true
	time.Sleep(150 * time.Millisecond)
	cancel()
	time.Sleep(10 * time.Millisecond)

	h := p.healthy.Load()
	if h == nil {
		t.Error("expected non-nil healthy list")
		return
	}
	l := len(*h)
	if l != 1 {
		t.Errorf("expected %d got %d", 1, l)
	}

}
