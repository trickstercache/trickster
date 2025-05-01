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

package alb

import (
	"context"
	"sync/atomic"
)

// responderClaim is a construct that allows only the first claimant
// to the response to act as the downstream responder
type responderClaim struct {
	lockVal  int64
	contexts []context.Context
}

func newResponderClaim(sz int) *responderClaim {
	contexts := make([]context.Context, sz)
	for i := 0; i < sz; i++ {
		contexts[i] = context.Background()
	}
	return &responderClaim{lockVal: -1, contexts: contexts}
}

func (rc *responderClaim) Claim(i int64) bool {
	if atomic.LoadInt64(&rc.lockVal) == i {
		return true
	}
	if atomic.CompareAndSwapInt64(&rc.lockVal, -1, i) {
		for j, ctx := range rc.contexts {
			if int64(j) != i {
				go ctx.Done()
			}
		}
		return true
	}
	rc.contexts[i].Done()
	return false
}
