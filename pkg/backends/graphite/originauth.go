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

package graphite

import (
	"fmt"
	"net/http"
	"strings"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
)

func originAuthHeader(g *gro.Options) (string, error) {
	return g.AuthHeader()
}

func appendsAuthorization(pc *po.Options) bool {
	for k := range pc.RequestHeaders {
		if strings.HasPrefix(k, "+") &&
			http.CanonicalHeaderKey(k[1:]) == headers.NameAuthorization {
			return true
		}
	}
	return false
}

func injectOriginAuth(paths po.List, auth string) error {
	if auth == "" {
		return nil
	}
	for _, pc := range paths {
		if pc == nil {
			continue
		}
		if appendsAuthorization(pc) {
			return fmt.Errorf("%w (path %s)", gro.ErrOriginAuthAppend, pc.Path)
		}
		if pc.ReplacesHeader(headers.NameAuthorization) {
			continue
		}
		if pc.RequestHeaders == nil {
			pc.RequestHeaders = make(map[string]string, 1)
		}
		pc.RequestHeaders[headers.NameAuthorization] = auth
		pc.RefreshIdentityKeyPart()
	}
	return nil
}
