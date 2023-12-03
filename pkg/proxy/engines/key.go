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

package engines

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/checksum/md5"
	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

// DeriveCacheKey calculates a query-specific keyname based on the user request
func (pr *proxyRequest) DeriveCacheKey(extra string) string {

	rsc := request.GetResources(pr.Request)
	pc := rsc.PathConfig

	if pc == nil {
		return md5.Checksum(pr.URL.Path + extra)
	}

	var qp url.Values
	r := pr.Request

	if pr.upstreamRequest != nil {
		r = pr.upstreamRequest
		if r.URL == nil {
			r.URL = pr.URL
		}
	}

	var b []byte
	if rsc.TimeRangeQuery != nil && rsc.TimeRangeQuery.TemplateURL != nil {
		qp = rsc.TimeRangeQuery.TemplateURL.Query()
	} else {
		var s string
		qp, s, _ = params.GetRequestValues(r)
		b = []byte(s)
	}

	if pc.KeyHasher != nil {
		var k string
		k, r.Body = pc.KeyHasher(r.URL.Path, qp, r.Header, r.Body, extra)
		return k
	}

	vals := make([]string, 0, (len(pc.CacheKeyParams) + len(pc.CacheKeyHeaders) + len(pc.CacheKeyFormFields)*2))

	if v := r.Header.Get(headers.NameAuthorization); v != "" {
		vals = append(vals, fmt.Sprintf("%s.%s.", headers.NameAuthorization, v))
	}

	// Append the http method to the slice for creating the derived cache key
	vals = append(vals, fmt.Sprintf("%s.%s.", "method", r.Method))

	if len(pc.CacheKeyParams) == 1 && pc.CacheKeyParams[0] == "*" {
		for p := range qp {
			vals = append(vals, fmt.Sprintf("%s.%s.", p, qp.Get(p)))
		}
	} else {
		for _, p := range pc.CacheKeyParams {
			if v := qp.Get(p); v != "" {
				vals = append(vals, fmt.Sprintf("%s.%s.", p, v))
			}
		}
	}

	for _, p := range pc.CacheKeyHeaders {
		if v := r.Header.Get(p); v != "" {
			vals = append(vals, fmt.Sprintf("%s.%s.", p, v))
		}
	}

	if methods.HasBody(r.Method) && pc.CacheKeyFormFields != nil && len(pc.CacheKeyFormFields) > 0 {
		ct := strings.ToLower(r.Header.Get(headers.NameContentType))
		if ct == headers.ValueXFormURLEncoded ||
			strings.HasPrefix(ct, headers.ValueMultipartFormData) || strings.HasPrefix(ct, headers.ValueApplicationJSON) {
			if strings.HasPrefix(ct, headers.ValueMultipartFormData) {
				pr.ParseMultipartForm(1024 * 1024)
			} else if strings.HasPrefix(ct, headers.ValueApplicationJSON) {
				var document map[string]interface{}
				err := json.Unmarshal(b, &document)
				if err == nil {
					for _, f := range pc.CacheKeyFormFields {
						v, err := deepSearch(document, f)
						if err == nil {
							if pr.Form == nil {
								pr.Form = url.Values{}
							}
							pr.Form.Set(f, v)
						}
					}
				}
			}
			r = request.SetBody(r, b)
		}

		for _, f := range pc.CacheKeyFormFields {
			if _, ok := pr.Form[f]; ok {
				if v := pr.FormValue(f); v != "" {
					vals = append(vals, fmt.Sprintf("%s.%s.", f, v))
				}
			}
		}
	}

	sort.Strings(vals)
	return md5.Checksum(pr.URL.Path + "." + strings.Join(vals, "") + extra)
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
			return "", errors.CouldNotFindKey(key)
		}
		if l != i {
			m, ok = v.(map[string]interface{})
			if !ok {
				return "", errors.CouldNotFindKey(key)
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
	return "", errors.CouldNotFindKey(key)
}
