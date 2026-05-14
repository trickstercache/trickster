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

package sql

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SetExtent interpolates the tokenized SQL query with concrete time bounds
func SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent, q *Query,
) {
	if extent == nil || r == nil || trq == nil || q == nil {
		return
	}
	stmt := interpolateTimeQuery(q, trq.TimestampDefinition, extent)
	isBody := methods.HasBody(r.Method)
	if isBody {
		request.SetBody(r, EncodeBody(r, stmt))
	} else {
		qi := r.URL.Query()
		qi.Set(ParamQuery, stmt)
		r.URL.RawQuery = qi.Encode()
	}
}

func interpolateTimeQuery(q *Query, tfd timeseries.FieldDefinition,
	extent *timeseries.Extent,
) string {
	start, end := formatTimestampValues(tfd, extent)
	tStart, tEnd := formatTimestampValues(
		timeseries.FieldDefinition{ProviderData1: tfd.ProviderData1}, extent)
	tsName := q.BaseTimestampFieldName
	if tsName == "" {
		tsName = tfd.Name
	}
	if tsName == "" {
		tsName = DefaultTimestampField
	}
	trange := fmt.Sprintf("%s >= %s AND %s < %s", tsName, start, tsName, end)
	out := strings.NewReplacer(
		tkRange, trange,
		tkTS1, tStart,
		tkTS2, tEnd,
	).Replace(q.TokenizedStatement)
	return out
}

func formatTimestampValues(tfd timeseries.FieldDefinition,
	extent *timeseries.Extent,
) (string, string) {
	dt := timeseries.FieldDataType(tfd.ProviderData1)
	switch dt {
	case timeseries.DateTimeUnixMilli:
		return strconv.FormatInt(extent.Start.UnixMilli(), 10),
			strconv.FormatInt(extent.End.UnixMilli(), 10)
	case timeseries.DateTimeUnixNano:
		return strconv.FormatInt(extent.Start.UnixNano(), 10),
			strconv.FormatInt(extent.End.UnixNano(), 10)
	case timeseries.DateTimeSQL:
		return "'" + extent.Start.UTC().Format("2006-01-02 15:04:05") + "'",
			"'" + extent.End.UTC().Format("2006-01-02 15:04:05") + "'"
	default:
		// RFC3339 — the default for InfluxDB v3
		return "'" + extent.Start.UTC().Format("2006-01-02T15:04:05Z") + "'",
			"'" + extent.End.UTC().Format("2006-01-02T15:04:05Z") + "'"
	}
}
