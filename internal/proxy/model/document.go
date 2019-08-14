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

package model

import "net/http"

//go:generate msgp

// HTTPDocument represents a full HTTP Response/Cache Document with unbuffered body
type HTTPDocument struct {
	StatusCode int                 `msg:"status_code"`
	Status     string              `msg:"status"`
	Headers    map[string][]string `msg:"headers"`
	Body       []byte              `msg:"body"`
}

// DocumentFromHTTPResponse returns an HTTPDocument from the provided HTTP Response and Body
func DocumentFromHTTPResponse(resp *http.Response, body []byte) *HTTPDocument {
	d := &HTTPDocument{}
	d.Headers = resp.Header
	d.StatusCode = resp.StatusCode
	d.Status = resp.Status
	d.Body = body
	return d
}
