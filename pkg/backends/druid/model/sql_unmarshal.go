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

package model

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func sameSQLColumns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, name := range a {
		counts[name]++
	}
	for _, name := range b {
		if counts[name] == 0 {
			return false
		}
		counts[name]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func sqlColumnIndex(columns []string, name string) int {
	for i, column := range columns {
		if column == name {
			return i
		}
	}
	for i, column := range columns {
		if strings.EqualFold(column, name) {
			return i
		}
	}
	return -1
}

var druidSQLTimestampLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseDruidSQLTimestamp(value any) (epoch.Epoch, error) {
	value = normalizeJSONValue(value)
	switch v := value.(type) {
	case string:
		for _, layout := range druidSQLTimestampLayouts {
			var parsed time.Time
			var err error
			if strings.Contains(layout, "Z07:00") {
				parsed, err = time.Parse(layout, v)
			} else {
				parsed, err = time.ParseInLocation(layout, v, time.UTC)
			}
			if err == nil && druidSQLUnixNanoRepresentable(parsed) {
				return epoch.Epoch(parsed.UnixNano()), nil
			}
		}
		if millis, err := strconv.ParseInt(v, 10, 64); err == nil {
			return druidSQLMillisEpoch(millis)
		}
	case int64:
		return druidSQLMillisEpoch(v)
	case uint64:
		millis, err := strconv.ParseInt(strconv.FormatUint(v, 10), 10, 64)
		if err == nil {
			return druidSQLMillisEpoch(millis)
		}
	}
	return 0, timeseries.ErrInvalidTimeFormat
}

func druidSQLUnixNanoRepresentable(value time.Time) bool {
	return time.Unix(0, value.UnixNano()).UTC().Equal(value.UTC())
}

func druidSQLMillisEpoch(millis int64) (epoch.Epoch, error) {
	if millis > math.MaxInt64/int64(time.Millisecond) ||
		millis < math.MinInt64/int64(time.Millisecond) {
		return 0, timeseries.ErrInvalidTimeFormat
	}
	return epoch.Epoch(millis * int64(time.Millisecond)), nil
}
