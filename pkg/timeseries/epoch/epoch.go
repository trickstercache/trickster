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

package epoch

import (
	"strconv"
	"time"

	lsql "github.com/trickstercache/trickster/v2/pkg/parsing/lex/sql"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

//go:generate go tool msgp

// Epoch represents an Epoch timestamp in Nanoseconds and has possible values
// between 1970/1/1 and 2262/4/12
type Epoch int64

// Epochs is a slice of type Epoch
type Epochs []Epoch

const BillionNS Epoch = 1000000000
const MillionNS Epoch = 1000000

// Format returns the epoch as a string in the specified format
func (e Epoch) Format(to timeseries.FieldDataType, quoteDateTimeSQL bool) string {
	switch to {
	case timeseries.DateTimeUnixSecs:
		return strconv.FormatInt(int64(e/BillionNS), 10)
	case timeseries.DateTimeUnixMilli:
		return strconv.FormatInt(int64(e/MillionNS), 10)
	case timeseries.DateTimeUnixNano:
		return strconv.FormatInt(int64(e), 10)
	case timeseries.DateTimeSQL, timeseries.DateSQL, timeseries.TimeSQL,
		timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano:
		return FormatTime(time.Unix(0, int64(e)), to, quoteDateTimeSQL)
	}
	return "0"
}

func FormatTime(t time.Time, to timeseries.FieldDataType, quoteDateTimeSQL bool) string {
	var q string
	if quoteDateTimeSQL {
		q = "'"
	}
	switch to {
	case timeseries.DateTimeUnixSecs:
		return strconv.FormatInt(t.Unix(), 10)
	case timeseries.DateTimeUnixMilli:
		return strconv.FormatInt(t.UnixMilli(), 10)
	case timeseries.DateTimeUnixNano:
		return strconv.FormatInt(t.UnixNano(), 10)
	case timeseries.DateTimeSQL:
		return q + t.UTC().Format(lsql.SQLDateTimeLayout) + q
	case timeseries.DateSQL:
		return q + t.UTC().Format(lsql.SQLDateLayout) + q
	case timeseries.TimeSQL:
		return q + t.UTC().Format(lsql.SQLTimeLayout) + q
	case timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano:
		return t.UTC().Format(time.RFC3339)
	}
	return "0"
}

func FromSecs(input int64) Epoch {
	return Epoch(input) * BillionNS
}

func FromMilliSecs(input int64) Epoch {
	return Epoch(input) * MillionNS
}

func FromNanoSecs(input int64) Epoch {
	return Epoch(input)
}
