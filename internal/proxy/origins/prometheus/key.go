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
	"strings"

	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func (c *Client) DeriveCacheKey(r *model.Request, extra string) string {

	cfg := c.Configuration()

	var hashParams []string
	var hashHeaders []string

	matchLen := -1
	for k, p := range cfg.PathsLookup {
		if strings.Index(r.URL.Path, k) > -1 && len(k) > matchLen {
			matchLen = len(k)
			hashParams = p.CacheKeyParams
			hashHeaders = p.CacheKeyHeaders
		}
	}

	params := r.URL.Query()
	vals := make([]string, 0, len(hashParams)+len(hashHeaders))

	for _, p := range hashParams {
		if v := params.Get(p); v != "" {
			vals = append(vals, v)
		}
	}

	for _, p := range hashHeaders {
		if v := r.Headers.Get(p); v != "" {
			vals = append(vals, v)
		}
	}

	return md5.Checksum(r.URL.Path + strings.Join(vals, "") + extra)
}
