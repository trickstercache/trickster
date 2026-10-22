/*
 * Copyright 2026 The Trickster Authors
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

package relay

import (
	"sync"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
)

const (
	renewNumerator   = 3
	renewDenominator = 4
)

type renewer struct {
	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

func (rn *renewer) schedule(p *message.Params, renew func()) {
	// the request is extended at three quarters of the time its sender gave (§9.20.16); an EXPIRES of 0 means it
	// no longer expires, and an answer without one, such as to a FORWARD update, leaves the renewal pending
	if !p.Has(message.ParamExpires) {
		return
	}
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.cancelLocked()
	if rn.stopped || p.Expires == 0 {
		return
	}
	rn.timer = time.AfterFunc(millis(p.Expires)/renewDenominator*renewNumerator, renew)
}

func (rn *renewer) cancel() {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.cancelLocked()
}

func (rn *renewer) cancelLocked() {
	if rn.timer != nil {
		rn.timer.Stop()
		rn.timer = nil
	}
}

func (rn *renewer) stop() {
	rn.mu.Lock()
	defer rn.mu.Unlock()
	rn.stopped = true
	rn.cancelLocked()
}
