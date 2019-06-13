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

package reverseproxycache

import (
	"sort"
	"strings"

	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey calculates a query-specific keyname based on the query in the user request
func (c Client) DeriveCacheKey(r *model.Request, extra string) string {

	params := r.TemplateURL.Query()
	ordered := make([]string, 0, len(params))
	for k := range params {
		ordered = append(ordered, k+"="+params.Get(k))
	}
	sort.Strings(ordered)

	return md5.Checksum(r.URL.Path + strings.Join(ordered, ",") + extra)
}
