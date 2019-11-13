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
	"fmt"
	"io/ioutil"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/golang/snappy"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/headers"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/context"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/md5"
)

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(c cache.Cache, key string) (*model.HTTPDocument, error) {

	inflate := c.Configuration().Compression
	if inflate {
		key += ".sz"
	}

	d := &model.HTTPDocument{}
	bytes, err := c.Retrieve(key, true)
	if err != nil {
		return d, err
	}

	if inflate {
		log.Debug("decompressing cached data", log.Pairs{"cacheKey": key})
		b, err := snappy.Decode(nil, bytes)
		if err == nil {
			bytes = b
		}
	}
	d.UnmarshalMsg(bytes)
	return d, nil
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(c cache.Cache, key string, d *model.HTTPDocument, ttl time.Duration) error {
	// Delete Date Header, http.ReponseWriter will insert as Now() on cache retrieval
	delete(d.Headers, "Date")
	bytes, err := d.MarshalMsg(nil)
	if err != nil {
		return err
	}

	if c.Configuration().Compression {
		key += ".sz"
		log.Debug("compressing cached data", log.Pairs{"cacheKey": key})
		bytes = snappy.Encode(nil, bytes)
	}

	return c.Store(key, bytes, ttl)
}

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

	if len(pc.CacheKeyFormFields) > 0 {
		if _, ok := methodsWithBody[r.ClientRequest.Method]; ok {
			ct := r.ClientRequest.Header.Get(headers.NameContentType)
			if ct == headers.ValueXFormUrlEncoded || ct == headers.ValueMultipartFormData {
				b, _ := ioutil.ReadAll(r.ClientRequest.Body)
				r.ClientRequest.Body = ioutil.NopCloser(bytes.NewReader(b))
				if ct == headers.ValueXFormUrlEncoded {
					r.ClientRequest.ParseForm()
				} else if ct == headers.ValueMultipartFormData {
					r.ClientRequest.ParseMultipartForm(1024 * 1024)
				}
				r.ClientRequest.Body = ioutil.NopCloser(bytes.NewReader(b))
			}
			for _, f := range pc.CacheKeyFormFields {
				fmt.Println("checking ", f)
				if v := r.ClientRequest.FormValue(f); f != "" {
					fmt.Printf("found [%s] value of [%s]", f, v)
					vals = append(vals, fmt.Sprintf("%s.%s.", f, v))
				}
			}
		}
	}

	sort.Strings(vals)
	return md5.Checksum(r.URL.Path + strings.Join(vals, "") + extra)
}
