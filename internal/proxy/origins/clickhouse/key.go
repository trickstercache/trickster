/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package clickhouse

import (
	"strconv"

	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey calculates a query-specific keyname based on the query in the user request
func (c *Client) DeriveCacheKey(r *model.Request, extra string) string {
	params := r.TemplateURL.Query()

	if r.TimeRangeQuery != nil && r.TimeRangeQuery.Step > 0 {
		extra += strconv.Itoa(int(r.TimeRangeQuery.Step))
	}

	return md5.Checksum(r.TemplateURL.Path + params.Get(upQuery) + extra)
}
