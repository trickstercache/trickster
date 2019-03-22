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
	"net/url"

	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func (c *Client) DeriveCacheKey(path string, params url.Values, prefix string, extra string) string {

	k := path
	// if we have a prefix, set it up
	if len(prefix) > 0 {
		k += prefix
	}

	if p, ok := params[upQuery]; ok {
		k += p[0]
	}

	if p, ok := params[upStep]; ok {
		k += p[0]
	}

	if p, ok := params[upTime]; ok {
		k += p[0]
	}

	if p, ok := params["authorization"]; ok {
		k += p[0]
	}

	if len(extra) > 0 {
		k += extra
	}

	return md5.Checksum(k)
}
