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
	"net/http"
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
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

// DeriveCacheKey calculates a query-specific keyname based on the user request
func (pr *proxyRequest) DeriveCacheKey(extra string) string {
	rsc := request.GetResources(pr.Request)
	pc := rsc.PathConfig

	if pc == nil {
		return md5.Checksum(pr.URL.Path + extra)
	}

	var qp url.Values
	useUR := pr.upstreamRequest != nil
	var r *http.Request

	if useUR {
		r = pr.upstreamRequest
		if r.URL == nil {
			r.URL = pr.URL
		}
	} else {
		r = pr.Request
	}

	var b []byte
	var ckeCnt int

	trq := rsc.TimeRangeQuery
	if trq != nil {
		ckeCnt = len(trq.CacheKeyElements)
		if trq.TemplateURL != nil {
			qp = trq.TemplateURL.Query()
		}
	}
	if qp == nil {
		qp, b, _ = params.GetRequestValues(r)
	}

	if pc.KeyHasher != nil {
		return pc.KeyHasher(r.URL.Path, qp, r.Header, b, trq, extra)
	}

	var k int
	vals := getCacheKeyValues()
	defer func() { putCacheKeyValues(vals) }()
	// Reserve capacity if needed
	needed := 2 + len(qp) + len(r.Header) + len(pc.CacheKeyFormFields) + ckeCnt
	if cap(vals) < needed {
		vals = append(vals[:0], make([]string, needed)...)[:0]
	}
	vals = vals[:needed] // Set length to allow direct indexing
	// overrides contains query data modified by the backend provider when
	// parsing the time range (e.g., a tokenized version of the query statement)
	var overrides map[string]string
	used := sets.NewStringSet()
	if trq != nil && len(trq.CacheKeyElements) > 0 {
		overrides = trq.CacheKeyElements
	} else {
		overrides = make(map[string]string)
	}

	if v := r.Header.Get(headers.NameAuthorization); v != "" {
		vals[k] = headers.NameAuthorization + "." + v + "."
		k++
	}
	// Append the http method to the slice for creating the derived cache key
	vals[k] = "method." + r.Method + "."
	k++

	if len(pc.CacheKeyParams) == 1 && pc.CacheKeyParams[0] == "*" {
		for p := range qp {
			if v, ok := overrides[p]; ok {
				vals[k] = p + "." + v + "."
				used.Set(p)
			} else {
				vals[k] = p + "." + qp.Get(p) + "."
			}
			k++
		}
	} else {
		for _, p := range pc.CacheKeyParams {
			if v, ok := overrides[p]; ok {
				vals[k] = p + "." + v + "."
				used.Set(p)
				k++
				continue
			}
			if v := qp.Get(p); v != "" {
				vals[k] = p + "." + v + "."
				k++
			}
		}
	}

	for _, p := range pc.CacheKeyHeaders {
		if v := r.Header.Get(p); v != "" {
			vals[k] = p + "." + v + "."
			k++
		}
	}

	var bodyWasProcessed bool
	if methods.HasBody(r.Method) && pc.CacheKeyFormFields != nil && len(pc.CacheKeyFormFields) > 0 {
		ct := strings.ToLower(r.Header.Get(headers.NameContentType))
		if strings.HasPrefix(ct, headers.ValueMultipartFormData) {
			pr.ParseMultipartForm(1024 * 1024)
			bodyWasProcessed = true
		} else if strings.HasPrefix(ct, headers.ValueApplicationJSON) {
			var document map[string]any
			if err := json.Unmarshal(b, &document); err == nil {
				for _, f := range pc.CacheKeyFormFields {
					if v, err := deepSearch(document, f); err == nil {
						if pr.Form == nil {
							pr.Form = url.Values{}
						}
						pr.Form.Set(f, v)
						bodyWasProcessed = true
					}
				}
			}
		}
		if bodyWasProcessed {
			for _, f := range pc.CacheKeyFormFields {
				if v, ok := overrides[f]; ok {
					used.Set(f)
					vals[k] = f + "." + v + "."
					k++
					continue
				}
				if _, ok := pr.Form[f]; ok {
					if v := pr.FormValue(f); v != "" {
						vals[k] = f + "." + v + "."
						k++
					}
				}
			}
		}
	}

	if trq != nil {
		for key, val := range overrides {
			if _, ok := used[key]; ok {
				continue
			}
			vals[k] = key + "." + val + "."
			k++
		}
	}
	vals = vals[:k]
	sort.Strings(vals)

	// Build final cache key using pooled strings.Builder
	sb := getCacheKeyBuilder()
	defer putCacheKeyBuilder(sb)
	sb.WriteString(pr.URL.Path)
	sb.WriteByte('.')
	for _, v := range vals {
		sb.WriteString(v)
	}
	sb.WriteString(extra)
	return md5.Checksum(sb.String())
}

func deepSearch(document map[string]any, key string) (string, error) {
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
			m, ok = v.(map[string]any)
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
