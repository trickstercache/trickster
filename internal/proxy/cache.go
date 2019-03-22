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

var magicHeader = []byte("sNaPpY")

// QueryCache ...
func QueryCache(c cache.Cache, key string) (*HTTPDocument, error) {

	inflate := c.Configuration().Compression
	if inflate {
		key += ".sz"
	}

	d := &HTTPDocument{}
	data, err := c.Retrieve(key)
	if err != nil {
		return d, err
	}

	if inflate {
		log.Debug("decompressing cached data", log.Pairs{"cacheKey": key})
		b, err := snappy.Decode(nil, data[6:])
		if err == nil {
			data = b
		}
	}
	_, err = d.UnmarshalMsg(data)
	return d, nil
}

// WriteCache ...
func WriteCache(c cache.Cache, key string, d *HTTPDocument, ttl int) error {
	// Delete Date Header, http.ReponseWriter will insert as Now() on cache retrieval
	delete(d.Headers, "Date")
	data, err := d.MarshalMsg(nil)
	if err != nil {
		return err
	}

	if c.Configuration().Compression {
		key += ".sz"
		log.Debug("compressing cached data", log.Pairs{"cacheKey": key})
		b := snappy.Encode(nil, data)
		data = append(magicHeader, b...)
	}

	return c.Store(key, data, int64(ttl))
}
