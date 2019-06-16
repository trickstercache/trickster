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
	"strings"
	"time"

	"github.com/golang/snappy"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/md5"
)

// Cache Lookup Results
const (
	CrKeyMiss    = "kmiss"
	CrRangeMiss  = "rmiss"
	CrHit        = "hit"
	CrPartialHit = "phit"
	CrPurge      = "purge"
)

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(c cache.Cache, key string) (*model.HTTPDocument, error) {

	inflate := c.Configuration().Compression
	if inflate {
		key += ".sz"
	}

	d := &model.HTTPDocument{}
	bytes, err := c.Retrieve(key)
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
	_, err = d.UnmarshalMsg(bytes)
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

// DeriveCacheKey calculates a query-specific keyname based on the prometheus query in the user request
func DeriveCacheKey(c model.Client, cfg *config.OriginConfig, r *model.Request, extra string) string {
	var hashParams []string
	var hashHeaders []string

	matchLen := -1
	for k, p := range cfg.PathsLookup {
		if strings.Index(r.URL.Path, k) > -1 && len(k) > matchLen {
			matchLen = len(k)
			hashParams = p.CacheKeyParams
			hashHeaders = p.CacheKeyHeaders
		}
	}

	params := r.URL.Query()
	vals := make([]string, 0, len(hashParams)+len(hashHeaders))

	for _, p := range hashParams {
		if v := params.Get(p); v != "" {
			vals = append(vals, v)
		}
	}

	for _, p := range hashHeaders {
		if v := r.Headers.Get(p); v != "" {
			vals = append(vals, v)
		}
	}

	return md5.Checksum(r.URL.Path + strings.Join(vals, "") + extra)
}
