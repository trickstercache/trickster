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

package middleware

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

const latencyHeaderName = "x-simulated-latency"

func processSimulatedLatency(w http.ResponseWriter, minMS, maxMS int) {
	if (minMS == 0 && maxMS == 0) || (minMS < 0 || maxMS < 0) {
		return
	}
	var ms int64
	if minMS >= maxMS {
		ms = int64(minMS)
	} else {
		ms = (rand.Int63() % int64(maxMS-minMS)) + int64(minMS)
	}
	if ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
	w.Header().Set(latencyHeaderName, strconv.FormatInt(ms, 10)+"ms")
}
