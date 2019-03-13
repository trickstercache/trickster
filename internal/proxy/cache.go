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

package proxy

import (
	"github.com/golang/snappy"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/util/log"
)

// Cache Lookup Results
const (
	CrKeyMiss    = "kmiss"
	CrRangeMiss  = "rmiss"
	CrHit        = "hit"
	CrPartialHit = "phit"
	CrPurge      = "purge"
)

// QueryCache ...
func QueryCache(c cache.Cache, key string) (*HTTPDocument, error) {

	inflate := c.Configuration().Compression
	if inflate {
		key += ".sz"
	}

	d := &HTTPDocument{}
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

// WriteCache ...
func WriteCache(c cache.Cache, key string, d *HTTPDocument, ttl int) error {
	// Delete Date Header, http.ReponseWriter will insert as Now() on cache retreival
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

	return c.Store(key, bytes, int64(ttl))
}
