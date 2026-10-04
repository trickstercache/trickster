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

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

//go:generate go tool msgp

// Epoch represents an Epoch timestamp in Nanoseconds and has possible values
// between 1970/1/1 and 2262/4/12
type Epoch int64

// Epochs is a slice of type Epoch
type Epochs []Epoch

const (
	BillionNS Epoch = 1000000000
	MillionNS Epoch = 1000000
)

// Format returns the epoch as a string in the specified format
func (e Epoch) Format(to timeseries.FieldDataType, quoteDateTimeSQL bool) string {
	var buf [40]byte
	return string(e.AppendFormat(buf[:0], to, quoteDateTimeSQL))
}

// AppendFormat appends the epoch to dst as Format writes it
func (e Epoch) AppendFormat(dst []byte, to timeseries.FieldDataType, quoteDateTimeSQL bool) []byte {
	switch to {
	case timeseries.DateTimeUnixSecs:
		return strconv.AppendInt(dst, int64(e/BillionNS), 10)
	case timeseries.DateTimeUnixMilli:
		return strconv.AppendInt(dst, int64(e/MillionNS), 10)
	case timeseries.DateTimeUnixNano:
		return strconv.AppendInt(dst, int64(e), 10)
	case timeseries.DateTimeSQL, timeseries.DateSQL, timeseries.TimeSQL:
		// as AppendTime writes it, without building a time.Time
		if quoteDateTimeSQL {
			dst = append(dst, '\'')
		}
		days, sod, _ := splitEpoch(e)
		if to != timeseries.TimeSQL {
			dst = appendDate(dst, days)
		}
		if to == timeseries.DateTimeSQL {
			dst = append(dst, ' ')
		}
		if to != timeseries.DateSQL {
			dst = appendClock(dst, sod)
		}
		if quoteDateTimeSQL {
			dst = append(dst, '\'')
		}
		return dst
	case timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano:
		return AppendCanonicalTime(dst, e, false, true)
	}
	return append(dst, '0')
}

func FormatTime(t time.Time, to timeseries.FieldDataType, quoteDateTimeSQL bool) string {
	var buf [40]byte
	return string(AppendTime(buf[:0], t, to, quoteDateTimeSQL))
}

// AppendTime appends t to dst as FormatTime writes it
func AppendTime(dst []byte, t time.Time, to timeseries.FieldDataType, quoteDateTimeSQL bool) []byte {
	var layout string
	switch to {
	case timeseries.DateTimeUnixSecs:
		return strconv.AppendInt(dst, t.Unix(), 10)
	case timeseries.DateTimeUnixMilli:
		return strconv.AppendInt(dst, t.UnixMilli(), 10)
	case timeseries.DateTimeUnixNano:
		return strconv.AppendInt(dst, t.UnixNano(), 10)
	case timeseries.DateTimeSQL:
		layout = timeconv.SQLDateTimeLayout
	case timeseries.DateSQL:
		layout = timeconv.SQLDateLayout
	case timeseries.TimeSQL:
		layout = timeconv.SQLTimeLayout
	case timeseries.DateTimeRFC3339, timeseries.DateTimeRFC3339Nano:
		return t.UTC().AppendFormat(dst, time.RFC3339)
	default:
		return append(dst, '0')
	}
	if quoteDateTimeSQL {
		dst = append(dst, '\'')
	}
	dst = t.UTC().AppendFormat(dst, layout)
	if quoteDateTimeSQL {
		dst = append(dst, '\'')
	}
	return dst
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
