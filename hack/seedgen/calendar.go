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

import "sort"

// The synthetic window is 12 weeks starting on a Monday. The database
// loaders shift it so the midpoint lands on the seed time.
const (
	windowStart   int64 = 1704067200 // 2024-01-01T00:00:00Z, a Monday
	secondsPerDay int64 = 86400
)

// Day counts stay untyped so they index slices and compare with loop ints.
const (
	windowDays = 84
	splitDay   = 42 // first day written to the second output file
)

var dowFactor = [7]float64{0.86, 0.95, 0.98, 1.00, 0.99, 0.96, 0.86} // Monday first

var hourShareWeekday = [24]float64{
	4.1, 2.8, 2.2, 1.6, 1.1, 1.1, 2.4, 3.7, 4.5, 4.5, 4.4, 4.6,
	5.0, 4.8, 5.0, 4.8, 4.1, 4.8, 5.9, 6.1, 5.7, 5.8, 5.8, 5.2,
}

var hourShareWeekend = [24]float64{
	5.6, 4.6, 3.8, 2.8, 1.8, 1.1, 1.3, 1.8, 2.6, 3.5, 4.2, 4.8,
	5.2, 5.3, 5.3, 5.1, 4.7, 4.6, 5.0, 5.3, 5.2, 5.4, 5.6, 5.4,
}

// Each day gets a minute-resolution CDF: the hourly shares are interpolated
// between hour centres so the curve is smooth, and every 5-minute bucket gets
// a hashed ±12 % factor so bucket counts wander the way real traffic does.
const (
	minutesPerDay = 24 * 60
	bucketMinutes = 5
	bucketNoise   = 0.24
)

var dayCDF [windowDays][minutesPerDay + 1]float64

func init() {
	for day := range windowDays {
		shares := &hourShareWeekday
		if isWeekend(day) {
			shares = &hourShareWeekend
		}
		cdf := &dayCDF[day]
		for m := range minutesPerDay {
			t := (float64(m)+0.5)/60 - 0.5 // hours, relative to hour centres
			if t < 0 {
				t += 24
			}
			h0 := int(t)
			w := t - float64(h0)
			d := shares[h0%24]*(1-w) + shares[(h0+1)%24]*w
			bucket := uint64(day*(minutesPerDay/bucketMinutes) + m/bucketMinutes) //nolint:gosec // nonnegative index
			d *= 1 + bucketNoise*(unit(bucket, saltBucketNoise)-0.5)
			cdf[m+1] = cdf[m] + d
		}
		total := cdf[minutesPerDay]
		for m := 1; m <= minutesPerDay; m++ {
			cdf[m] /= total
		}
		cdf[minutesPerDay] = 1
	}
}

func isWeekend(day int) bool { return day%7 >= 5 }

// rowsForDay is base × day-of-week factor × ±4 % hashed jitter.
func rowsForDay(day, base int) int {
	j := 1 + 0.08*(unit(uint64(day), saltDayJitter)-0.5) //nolint:gosec // day is a nonnegative index
	return int(float64(base)*dowFactor[day%7]*j + 0.5)
}

// secondOfDay maps row k of n on the given day to a second in [0, 86400) by
// inverting the day's minute CDF at u=(k+0.5)/n plus a sub-slot jitter, so
// rows stay time-ordered and the day's histogram follows the smoothed curve.
func secondOfDay(day, k, n int, row uint64) int {
	cdf := &dayCDF[day]
	u := (float64(k) + 0.5) / float64(n)
	m := min(sort.Search(minutesPerDay, func(i int) bool { return cdf[i+1] > u }), minutesPerDay-1)
	width := cdf[m+1] - cdf[m]
	frac := (u - cdf[m]) / width
	slot := 1 / (float64(n) * width) // one row's share of the minute, in minute units
	frac += (unit(row, saltTimeOfDay) - 0.5) * slot
	if frac < 0 {
		frac = 0
	} else if frac >= 1 {
		frac = 0.999999
	}
	return m*60 + int(frac*60)
}
