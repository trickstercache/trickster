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

package influxdb

import (
	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/util/md5"
)

// DeriveCacheKey ...
func (c Client) DeriveCacheKey(r *proxy.Request, extra string) string {

	k := r.TemplateURL.Path
	params := r.TemplateURL.Query()

	if p, ok := params[upDB]; ok {
		k += p[0]
	}

	if p, ok := params[upQuery]; ok {
		k += p[0]
	}

	// Epoch Precision Param
	if p, ok := params[upEpoch]; ok {
		k += "." + p[0]
	}

	// Username Param

	if p, ok := params["u"]; ok {
		k += "." + p[0]
	}

	// Password Param
	if p, ok := params["p"]; ok {
		k += "." + p[0]
	}

	if len(extra) > 0 {
		k += extra
	}
	return md5.Checksum(k)
}
