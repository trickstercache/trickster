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

func processSimulatedLatency(w http.ResponseWriter, min, max time.Duration) {
	if (min == 0 && max == 0) || (min < 0 || max < 0) {
		return
	}

	minMS := min.Milliseconds()
	maxMS := max.Milliseconds()

	var ms int64
	if minMS >= maxMS {
		ms = minMS
	} else {
		ms = (rand.Int63()%maxMS - minMS) + minMS // #nosec G404 -- we are OK with a weak random source, random-ish enough for our purposes, no security risk
	}
	if ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
	w.Header().Set(latencyHeaderName, strconv.FormatInt(ms, 10)+"ms")
}
