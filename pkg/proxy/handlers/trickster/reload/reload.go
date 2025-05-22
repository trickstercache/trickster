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

package reload

import (
	"net/http"

	"github.com/trickstercache/trickster/v2/pkg/config/reload"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

// HandlerFunc will reload the running configuration if it has changed
func HandlerFunc(f reload.Reloader) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		didReload, _ := f("handler")
		w.Header().Set(headers.NameContentType, headers.ValueTextPlain)
		w.Header().Set(headers.NameCacheControl, headers.ValueNoCache)
		w.WriteHeader(http.StatusOK)
		if didReload {
			w.Write([]byte(reload.ConfigReloadedText))
		} else {
			w.Write([]byte(reload.ConfigNotReloadedText))
		}
	}
}
