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

package rule

import (
	"net/http"
	"strings"
)

// Handler processes the HTTP request through the rules engine
func (c *Client) Handler(w http.ResponseWriter, r *http.Request) {

	// TODO: Connect the logic dots that actually determine the real router
	router := c.rule.defaultRouter

	if strings.HasPrefix(r.URL.Path, c.pathPrefix) {
		r.URL.Path = strings.Replace(r.URL.Path, c.pathPrefix, "", 1)
	}
	router.ServeHTTP(w, r)
}
