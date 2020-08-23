/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
	"time"

	"github.com/tricksterproxy/trickster/pkg/timeseries"
)

// Common URL Parameter Names
const (
	upQuery = "query"
)

// SetExtent will change the upstream request query to use the provided Extent
func (c *Client) SetExtent(r *http.Request, trq *timeseries.TimeRangeQuery, extent *timeseries.Extent) {

	if extent == nil || r == nil || trq == nil {
		return
	}

	p := r.URL.Query()
	q := trq.TemplateURL.Query().Get(upQuery)

	// Force gzip compression since Brotli is broken on CH 20.3
	// See https://github.com/ClickHouse/ClickHouse/issues/9969
	r.Header.Set("Accept-Encoding", "gzip")

	if q != "" {
		p.Set(upQuery, interpolateTimeQuery(q, trq.TimestampDefinition.Name, extent, trq.Step))
	}

	r.URL.RawQuery = p.Encode()
}

func interpolateTimeQuery(template string, tsFieldName string, extent *timeseries.Extent, step time.Duration) string {
	rangeCondition := fmt.Sprintf("%s BETWEEN %d AND %d", tsFieldName, extent.Start.Unix(), extent.End.Unix())
	return strings.Replace(strings.Replace(strings.Replace(template, tkRange, rangeCondition, -1),
		tkTS1, strconv.FormatInt(extent.Start.Unix(), 10), -1), tkTS2, strconv.FormatInt(extent.End.Unix(), 10), -1)
}
