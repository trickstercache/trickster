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

package engines

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/md5"
)

var methodsWithBody = map[string]bool{http.MethodPut: true, http.MethodPost: true, http.MethodPatch: true}

// DeriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func DeriveCacheKey(r *model.Request, apc *config.PathConfig, extra string) string {

	pc := context.PathConfig(r.ClientRequest.Context())
	if apc != nil {
		pc = apc
	}
	if pc == nil {
		return md5.Checksum(r.URL.Path + extra)
	}

	params := r.URL.Query()

	if pc.KeyHasher != nil && len(pc.KeyHasher) == 1 {
		return pc.KeyHasher[0](r.URL.Path, params, r.Headers, r.ClientRequest.Body, extra)
	}

	vals := make([]string, 0, (len(pc.CacheKeyParams) + len(pc.CacheKeyHeaders) + len(pc.CacheKeyFormFields)*2))

	if v := r.Headers.Get(headers.NameAuthorization); v != "" {
		vals = append(vals, fmt.Sprintf("%s.%s.", headers.NameAuthorization, v))
	}

	// Append the http method to the slice for creating the derived cache key
	vals = append(vals, fmt.Sprintf("%s.%s.", "method", r.HTTPMethod))

	if len(pc.CacheKeyParams) == 1 && pc.CacheKeyParams[0] == "*" {
		for p := range params {
			vals = append(vals, fmt.Sprintf("%s.%s.", p, params.Get(p)))
		}
	} else {
		for _, p := range pc.CacheKeyParams {
			if v := params.Get(p); v != "" {
				vals = append(vals, fmt.Sprintf("%s.%s.", p, v))
			}
		}
	}

	for _, p := range pc.CacheKeyHeaders {
		if v := r.Headers.Get(p); v != "" {
			vals = append(vals, fmt.Sprintf("%s.%s.", p, v))
		}
	}

	if _, ok := methodsWithBody[r.ClientRequest.Method]; ok && len(pc.CacheKeyFormFields) > 0 {
		ct := r.ClientRequest.Header.Get(headers.NameContentType)
		if ct == headers.ValueXFormUrlEncoded || strings.HasPrefix(ct, headers.ValueMultipartFormData) || ct == headers.ValueApplicationJSON {
			b, _ := ioutil.ReadAll(r.ClientRequest.Body)
			r.ClientRequest.Body = ioutil.NopCloser(bytes.NewReader(b))
			if ct == headers.ValueXFormUrlEncoded {
				r.ClientRequest.ParseForm()
			} else if strings.HasPrefix(ct, headers.ValueMultipartFormData) {
				r.ClientRequest.ParseMultipartForm(1024 * 1024)
			} else if ct == headers.ValueApplicationJSON {
				var document map[string]interface{}
				err := json.Unmarshal(b, &document)
				if err == nil {
					for _, f := range pc.CacheKeyFormFields {
						v, err := deepSearch(document, f)
						if err == nil {
							if r.ClientRequest.Form == nil {
								r.ClientRequest.Form = url.Values{}
							}
							r.ClientRequest.Form.Set(f, v)
						}
					}
				}
			}
			r.ClientRequest.Body = ioutil.NopCloser(bytes.NewReader(b))
		}

		for _, f := range pc.CacheKeyFormFields {
			if _, ok := r.ClientRequest.Form[f]; ok {
				if v := r.ClientRequest.FormValue(f); v != "" {
					vals = append(vals, fmt.Sprintf("%s.%s.", f, v))
				}
			}
		}
	}

	sort.Strings(vals)
	return md5.Checksum(r.URL.Path + strings.Join(vals, "") + extra)
}

func deepSearch(document map[string]interface{}, key string) (string, error) {

	if key == "" {
		return "", fmt.Errorf("invalid key name: %s", key)
	}
	parts := strings.Split(key, "/")
	m := document
	l := len(parts) - 1
	for i, p := range parts {
		v, ok := m[p]
		if !ok {
			return "", fmt.Errorf("could not find key: %s", key)
		}
		if l != i {
			m, ok = v.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("could not find key: %s", key)
			}
			continue
		}

		if s, ok := v.(string); ok {
			return s, nil
		}

		if i, ok := v.(float64); ok {
			return strconv.FormatFloat(i, 'f', 4, 64), nil
		}

		if b, ok := v.(bool); ok {
			return fmt.Sprintf("%t", b), nil
		}

	}
	return "", fmt.Errorf("could not find key: %s", key)
}
