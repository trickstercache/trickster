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

package rule

import (
	"net/http"
)

// Handler processes the HTTP request through the rules engine
func (c *Client) Handler(w http.ResponseWriter, r *http.Request) {
	var router http.Handler
	// Execute the rule to determine where to send this request next
	router, r, _ = c.rule.evaluatorFunc(r)

	// Forward the request
	router.ServeHTTP(w, r)
}
