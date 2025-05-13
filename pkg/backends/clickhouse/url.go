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

package clickhouse

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// Common URL Parameter Names
const (
	upQuery = "query"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery,
	extent *timeseries.Extent) {

	if extent == nil || r == nil || trq == nil {
		return
	}

	qi := r.URL.Query()
	isBody := methods.HasBody(r.Method)

	sqlQuery := interpolateTimeQuery(trq.Statement, trq.TimestampDefinition.Name,
		trq.TimestampDefinition.ProviderData1,
		trq.TimestampDefinition.ProviderData2, extent)
	if isBody {
		request.SetBody(r, []byte(sqlQuery))
	} else {
		qi.Set(upQuery, sqlQuery)
		r.URL.RawQuery = qi.Encode()
	}

}

func interpolateTimeQuery(template string, tsFieldName string,
	resultTimeFormat int, dbTimeFormat int,
	extent *timeseries.Extent) string {

	var start, end, tStart, tEnd int64

	switch resultTimeFormat {
	case 1:
		tStart = extent.Start.UnixNano() / 1000000
		tEnd = extent.End.UnixNano() / 1000000
	default:
		tStart = extent.Start.Unix()
		tEnd = extent.End.Unix()
	}

	switch dbTimeFormat {
	case 1:
		start = extent.Start.UnixNano() / 1000000
		end = extent.End.UnixNano() / 1000000
	default:
		start = extent.Start.Unix()
		end = extent.End.Unix()
	}

	trange := fmt.Sprintf("%s BETWEEN %d AND %d", tsFieldName, tStart, tEnd)
	return strings.NewReplacer(
		tkRange, trange,
		tkTS1, strconv.FormatInt(start, 10),
		tkTS2, strconv.FormatInt(end, 10),
		tkFormat, "TSVWithNamesAndTypes",
	).Replace(template)
}
