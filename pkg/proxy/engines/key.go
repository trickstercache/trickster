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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/params"
	proxyurls "github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// ComposeCacheKey assembles a per-backend cache key. The name prefix isolates
// keys when cache_name is shared across backends. engine is "" for plain HTTP
// proxy, "opc" for object cache, "dpc" for delta proxy cache.
func ComposeCacheKey(name, prefix, engine, suffix string) string {
	if engine == "" {
		return name + "." + prefix + "." + suffix
	}
	return name + "." + prefix + "." + engine + "." + suffix
}

// DerivePathCacheKey derives the cache key of a request for path with no
// keyed parameters, headers, body fields, or overrides (purge-by-path).
// identity is the matched path config's IdentityKeyPart, or empty.
func DerivePathCacheKey(path, method, identity string) string {
	var kb keyBuilder
	return kb.sum(path, method, "", "", identity, "")
}

// cache-key component classes: each keyed element is hashed with the class of
// its source, so elements of different kinds sharing a name cannot collide
const (
	compAuth     byte = 'a' // the effective client Authorization credential
	compForm     byte = 'f' // a body field named in cache_key_form_fields
	compHeader   byte = 'h' // a header named in cache_key_headers
	compOverride byte = 'o' // a provider-supplied cache key element
	compParam    byte = 'p' // a query parameter named in cache_key_params
)

type keyComponent struct {
	class byte
	name  string
	value string
}

// keyBuilder accumulates typed cache-key components and streams them, sorted
// and length-prefixed, into one sha256 fingerprint
type keyBuilder struct {
	comps []keyComponent
}

func (b *keyBuilder) add(class byte, name, value string) {
	b.comps = append(b.comps, keyComponent{class: class, name: name, value: value})
}

// each value is length-prefixed so ["a.b"] and ["a", "b"] cannot collide
func (b *keyBuilder) addValues(class byte, name string, values []string) {
	var sb strings.Builder
	var sizes [binary.MaxVarintLen64]byte
	for _, v := range values {
		n := binary.PutUvarint(sizes[:], uint64(len(v)))
		sb.Write(sizes[:n])
		sb.WriteString(v)
	}
	b.add(class, name, sb.String())
}

// preamble segments hash in order, then the sorted components; every name and
// value is length-prefixed, so no delimiter in the data can shift a boundary
func (b *keyBuilder) sum(preamble ...string) string {
	slices.SortFunc(b.comps, func(x, y keyComponent) int {
		if x.class != y.class {
			return int(x.class) - int(y.class)
		}
		if v := strings.Compare(x.name, y.name); v != 0 {
			return v
		}
		return strings.Compare(x.value, y.value)
	})
	h := sha256.New()
	var buf []byte
	writeStr := func(s string) {
		buf = binary.AppendUvarint(buf[:0], uint64(len(s)))
		buf = append(buf, s...)
		h.Write(buf)
	}
	for _, s := range preamble {
		writeStr(s)
	}
	for _, c := range b.comps {
		buf = append(buf[:0], c.class)
		h.Write(buf)
		writeStr(c.name)
		writeStr(c.value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DeriveCacheKey calculates a query-specific keyname based on the user
// request, keying each element on its effective (post-override) upstream value
func (pr *proxyRequest) DeriveCacheKey(extra string) string {
	pc := pr.rsc.PathConfig
	upstreamKeyPart := pr.upstreamURLRewriteCacheKey()

	if pc == nil {
		var kb keyBuilder
		return kb.sum(pr.URL.Path, upstreamKeyPart,
			pr.corsCacheKeyPart(pr.Request), extra)
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

	trq := pr.rsc.TimeRangeQuery
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
		key := pc.KeyHasher(r.URL.Path, qp, r.Header, b, trq, extra)
		if cors := pr.corsCacheKeyPart(r); upstreamKeyPart != "" || cors != "" {
			var kb keyBuilder
			return kb.sum(key, upstreamKeyPart, cors)
		}
		return key
	}

	kb := keyBuilder{comps: make([]keyComponent, 0, 2+len(qp)+
		len(pc.CacheKeyHeaders)+len(pc.CacheKeyFormFields)+ckeCnt)}
	// overrides contains query data modified by the backend provider when
	// parsing the time range (e.g., a tokenized version of the query statement)
	var overrides map[string]string
	if trq != nil {
		overrides = trq.CacheKeyElements
	}

	if v := r.Header.Get(headers.NameAuthorization); v != "" &&
		!pc.ReplacesHeader(headers.NameAuthorization) {
		kb.add(compAuth, headers.NameAuthorization, v)
	}

	if len(pc.CacheKeyParams) == 1 && pc.CacheKeyParams[0] == "*" {
		for p := range qp {
			if _, ok := overrides[p]; ok {
				continue
			}
			if pc.ReplacesParam(p) {
				continue
			}
			kb.addValues(compParam, p, qp[p])
		}
	} else {
		for _, p := range pc.CacheKeyParams {
			if _, ok := overrides[p]; ok {
				continue
			}
			if pc.ReplacesParam(p) {
				continue
			}
			if vv := qp[p]; len(vv) > 0 {
				kb.addValues(compParam, p, vv)
			}
		}
	}

	for _, p := range pc.CacheKeyHeaders {
		cn := http.CanonicalHeaderKey(p)
		if pc.ReplacesHeader(cn) {
			continue
		}
		if v := r.Header.Get(p); v != "" {
			kb.add(compHeader, cn, v)
		}
	}

	var bodyWasProcessed bool
	if methods.HasBody(r.Method) && pc.CacheKeyFormFields != nil && len(pc.CacheKeyFormFields) > 0 {
		ct := strings.ToLower(r.Header.Get(headers.NameContentType))
		if strings.HasPrefix(ct, headers.ValueMultipartFormData) {
			const maxMultipartFormBytes = 1024 * 1024
			pr.Body = http.MaxBytesReader(nil, pr.Body, maxMultipartFormBytes)
			if err := pr.ParseMultipartForm(maxMultipartFormBytes); err == nil { // #nosec G120 -- body bounded by MaxBytesReader above; gosec taint does not track Body field
				bodyWasProcessed = true
			}
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
				if _, ok := overrides[f]; ok {
					continue
				}
				if pc.ReplacesParam(f) {
					continue
				}
				if _, ok := pr.Form[f]; ok {
					if v := pr.FormValue(f); v != "" {
						kb.add(compForm, f, v)
					}
				}
			}
		}
	}

	for key, val := range overrides {
		kb.add(compOverride, key, val)
	}

	// the identity part is the precomputed digest of the configured
	// request_headers/request_params, so rotating either rotates the key
	return kb.sum(pr.URL.Path, r.Method, upstreamKeyPart,
		pr.corsCacheKeyPart(r), pc.IdentityKeyPart(), extra)
}

func (pr *proxyRequest) upstreamURLRewriteCacheKey() string {
	if pr == nil || pr.rsc == nil || pr.rsc.BackendOptions == nil {
		return ""
	}
	o := pr.rsc.BackendOptions
	base := proxyurls.FromParts(o.Scheme, o.Host, "", "", "")
	return proxyurls.UpstreamURLRewriteCacheKey(pr.Request, base)
}

func (pr *proxyRequest) corsCacheKeyPart(r *http.Request) string {
	if pr == nil || pr.rsc == nil || pr.rsc.FrontendCORS == nil ||
		!pr.rsc.FrontendCORS.PreservesOrigin() || r == nil {
		return ""
	}
	return ".origin." + r.Header.Get(headers.NameOrigin)
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
			return strconv.FormatBool(b), nil
		}
	}
	return "", errors.CouldNotFindKey(key)
}
