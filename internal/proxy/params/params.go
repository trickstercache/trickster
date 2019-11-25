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

package params

import "net/url"

// UpdateParams updates the provided query parameters collection with the provided updates
func UpdateParams(params url.Values, updates map[string]string) {
	if params == nil || updates == nil || len(updates) == 0 {
		return
	}
	for k, v := range updates {
		if len(k) == 0 {
			continue
		}
		if k[0:1] == "-" {
			k = k[1:]
			params.Del(k)
			continue
		}
		if k[0:1] == "+" {
			k = k[1:]
			params.Add(k, v)
			continue
		}
		params.Set(k, v)
	}
}
