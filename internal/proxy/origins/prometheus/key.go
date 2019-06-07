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

package prometheus

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func (c *Client) DeriveCacheKey(r *model.Request, extra string) string {
	params := r.URL.Query()

	switch r.HandlerName {
	case "SeriesHandler":
		var matchString string
		if p, ok := params[upMatch]; ok {
			sort.Strings(p)
			matchString = strings.Join(p, ",")
		}
		fmt.Println(r.HandlerName)
		return md5.Checksum(r.URL.Path + params.Get(upStart) + params.Get(upEnd) + matchString + extra)
	}

	return md5.Checksum(r.URL.Path + params.Get(upQuery) + params.Get(upStep) + params.Get(upTime) + extra)
}
