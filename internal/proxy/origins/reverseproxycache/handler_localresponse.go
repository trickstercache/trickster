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
	"net/http"

	th "github.com/Comcast/trickster/internal/proxy/handlers"
	"github.com/Comcast/trickster/internal/proxy/model"
)

// LocalResponseHandler
func (c Client) LocalResponseHandler(w http.ResponseWriter, r *http.Request) {
	th.HandleLocalResponse(w, r, model.NewRequest(c.Configuration(), "LocalResponseHandler", nil, r.Header, c.config.Timeout, r, c.webClient))
}