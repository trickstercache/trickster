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

package middleware

import (
	"net/http"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/context"
	"github.com/Comcast/trickster/internal/proxy/origins"
	"github.com/Comcast/trickster/internal/proxy/request"
	tl "github.com/Comcast/trickster/internal/util/log"
)

// WithResourcesContext ...
func WithResourcesContext(client origins.Client, oc *config.OriginConfig,
	c cache.Cache, p *config.PathConfig, l *tl.TricksterLogger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var resources *request.Resources
		if c == nil {
			resources = request.NewResources(oc, p, nil, nil, client, l)
		} else {
			resources = request.NewResources(oc, p, c.Configuration(), c, client, l)
		}
		next.ServeHTTP(w, r.WithContext(context.WithResources(r.Context(), resources)))
	})
}
