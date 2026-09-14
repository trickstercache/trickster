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

package main

import "math"

// Every value in the output is a pure function of (row number, salt), so
// each column can be re-themed or re-tuned without disturbing the others.
const (
	saltDayJitter   uint64 = 0x01
	saltTimeOfDay   uint64 = 0x02
	saltPickup      uint64 = 0x03
	saltDropoff     uint64 = 0x04
	saltCabType     uint64 = 0x05
	saltDistance    uint64 = 0x06
	saltDuration    uint64 = 0x07
	saltPassenger   uint64 = 0x08
	saltVendor      uint64 = 0x09
	saltPayment     uint64 = 0x0a
	saltTip         uint64 = 0x0b
	saltTolls       uint64 = 0x0c
	saltCoords      uint64 = 0x0d
	saltIDGap       uint64 = 0x0e
	saltStoreFwd    uint64 = 0x0f
	saltZoneToken   uint64 = 0x10
	saltRateCode    uint64 = 0x11
	saltSurcharge   uint64 = 0x12
	saltCentroid    uint64 = 0x13
	saltBucketNoise uint64 = 0x14
)

// mix returns a splitmix64-finalized 64-bit hash of (n, salt).
func mix(n, salt uint64) uint64 {
	x := n*0x9E3779B97F4A7C15 ^ salt*0xD1B54A32D192ED03
	x = (x ^ (x >> 30)) * 0xBF58476D1CE4E5B9
	x = (x ^ (x >> 27)) * 0x94D049BB133111EB
	return x ^ (x >> 31)
}

// unit returns a deterministic value in [0,1) with 53 bits of precision.
func unit(n, salt uint64) float64 { return float64(mix(n, salt)>>11) / (1 << 53) }

// gauss returns a deterministic standard-normal sample (one Box-Muller half).
func gauss(n, salt uint64) float64 {
	u1 := 1 - unit(n, salt)
	u2 := unit(n, salt^0xA5A5A5A5)
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}
