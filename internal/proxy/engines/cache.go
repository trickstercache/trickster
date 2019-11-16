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
	"github.com/pkg/errors"
	"strconv"
	"strings"
	"time"

	"github.com/golang/snappy"

	"github.com/Comcast/trickster/internal/cache"
	"github.com/Comcast/trickster/internal/proxy/model"
	"github.com/Comcast/trickster/internal/util/log"
)

// QueryCache queries the cache for an HTTPDocument and returns it
func QueryCache(c cache.Cache, key string, byteRange model.Ranges) (*model.HTTPDocument, error) {

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
	if byteRange != nil && len(byteRange) > 0 {
		d.UpdatedQueryRange = d.Ranges.CalculateDelta(d, byteRange)
		// Cache hit
		if d.UpdatedQueryRange == nil {
			body := d.Body
			d.Body = make([]byte, len(d.Body))
			for _, v := range byteRange {
				copy(d.Body[v.Start:v.End], body[v.Start:v.End])
			}
		}
	}
	return d, nil
}

// WriteCache writes an HTTPDocument to the cache
func WriteCache(c cache.Cache, key string, d *model.HTTPDocument, ttl time.Duration, byteRange model.Ranges) error {
	// Delete Date Header, http.ReponseWriter will insert as Now() on cache retrieval
	delete(d.Headers, "Date")
	if byteRange == nil {
		ranges := make(model.Ranges, 1)
		if d.Headers["Content-Length"] != nil {
			end, err := strconv.Atoi(d.Headers["Content-Length"][0])
			if err != nil {
				log.Error("Couldn't convert the content length to a number", log.Pairs{"content length": end})
				return err
			}
			fullByteRange := model.Range{Start: 0, End: end}
			ranges[0] = fullByteRange
			d.Ranges = ranges
		}
	}
	bytes, err := d.MarshalMsg(nil)
	if err != nil {
		return err
	}

	if byteRange != nil {
		// Content-Range
		doc, err := QueryCache(c, key, byteRange)
		if err != nil {
			// First time, Doesn't exist in the cache
			// Example -> Content-Range: bytes 0-1023/146515
			// length = 0-1023/146515
			if d.Headers["Content-Range"] == nil {
				return errors.New("No Content-Range in the request")
			}
			length := d.Headers["Content-Range"][0]
			index := 0
			for k, v := range length {
				if '/' == v {
					index = k
					break
				}
			}
			// length, after this, will have 146515
			length = length[index+1:]
			totalSize, err := strconv.Atoi(length)
			if err != nil {
				log.Error("Couldn't convert to a valid length", log.Pairs{"length": length})
				return err
			}
			fullSize := make([]byte, totalSize)

			// Multipart request
			if d.Headers["Content-Type"] != nil {
				if strings.Contains(d.Headers["Content-Type"][0], "multipart/byteranges; boundary=") {
					for _, v2 := range byteRange {
						start := v2.Start
						end := v2.End
						copy(fullSize[start:end], d.Body[start:end])
					}
				} else {
					copy(fullSize[byteRange[0].Start:byteRange[0].End], d.Body)
				}
			}

			d.Body = fullSize
			d.Ranges = byteRange
			bytes, err = d.MarshalMsg(nil)
			if err != nil {
				return err
			}
			return c.Store(key, bytes, ttl)
		}
		// Case when the key was found in the cache: store only the required part
		for _, v3 := range doc.UpdatedQueryRange {
			doc.Ranges[len(doc.Ranges)-1] = model.Range{Start: v3.Start, End: v3.End}
		}
		doc.UpdatedQueryRange = nil
		bytes, err = d.MarshalMsg(nil)
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
	if c.Configuration().Compression {
		key += ".sz"
		log.Debug("compressing cached data", log.Pairs{"cacheKey": key})
		bytes = snappy.Encode(nil, bytes)
	}
	return c.Store(key, bytes, ttl)
}
