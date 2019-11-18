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

package model

import (
	"sort"
	"strconv"
	"strings"

	"github.com/Comcast/trickster/internal/util/log"
)

//go:generate msgp

// Range represents the start and end for a byte range object
type Range struct {
	Start int `msg:"start"`
	End   int `msg:"end"`
}

// Ranges represents a slice of type Range
type Ranges []Range

// HTTPDocument represents a full HTTP Response/Cache Document with unbuffered body
type HTTPDocument struct {
	StatusCode    int                 `msg:"status_code"`
	Status        string              `msg:"status"`
	Headers       map[string][]string `msg:"headers"`
	Body          []byte              `msg:"body"`
	CachingPolicy *CachingPolicy      `msg:"caching_policy"`
	// UpdatedQueryRange is used to send the query to upstream in case of cache miss
	UpdatedQueryRange Ranges `msg:"updated_query_range"`
	// Ranges is the ranges of the doc that are in the cache currently
	Ranges Ranges `msg:"ranges"`
}

// CalculateDelta calculates the delta in the byte ranges and returns the range
// that we need to query upstream
func (r Ranges) CalculateDelta(d *HTTPDocument, byteRange Ranges) Ranges {
	if d.Headers["Content-Length"] == nil {
		log.Error("Got an empty content length!", log.Pairs{"Content-Length": d.Headers["Content-Length"]})
		return nil
	}
	if d.Headers["Content-Range"] == nil {
		log.Error("Got an empty content range!", log.Pairs{"Content-Range": d.Headers["Content-Range"]})
		return nil
	}
	i := strings.LastIndex(d.Headers["Content-Range"][0], "/")
	i = i + 1
	totalLength, err := strconv.Atoi(d.Headers["Content-Range"][0][i:])
	if err != nil {
		log.Error("Couldn't convert content range to an int!", log.Pairs{"err": err})
		return nil
	}
	hit := false
	sort.SliceStable(r, func(i, j int) bool {
		return r[i].Start < r[j].Start
	})

	updatedquery := make([]Range, 0)

	for _, val := range byteRange {
		start := val.Start
		end := val.End
		if start < 0 || end > totalLength {
			log.Error("Start or End out of bounds!", log.Pairs{"start": start, "end": end})
			return nil
		}
		if (r[0].Start > end) ||
			(r[len(r)-1].End < start) {
			//New retrieve from upstream
			r = append(r, Range{Start: start, End: end})
			d.Ranges = r
			if d.UpdatedQueryRange == nil {
				d.UpdatedQueryRange = make(Ranges, 0)
			}
			updatedquery = append(updatedquery, Range{Start: start, End: end})
			hit = false
		} else {
			// v has what is available in our cache currently
			for k, v := range r {
				if start > v.Start && end < v.End {
					// Just return the intermediate bytes from the cache, since we have everything in the cache
					hit = true
				} else if start > v.Start && end > v.End {
					if start < v.End {
						start = v.End
					}
					hit = false
					updatedquery = append(updatedquery, Range{Start: start, End: end})
				} else if start < v.Start && end < v.Start {
					// Just return the same start and end, since we have a full cache miss
					hit = false
					updatedquery = append(updatedquery, Range{Start: start, End: end})
				} else if start < v.Start && end < v.End {
					if end > v.Start {
						end = v.Start
					}
					hit = false
					updatedquery = append(updatedquery, Range{Start: start, End: end})
				} else {
					hit = false
					if k != 0 {
						updatedquery[k].End = v.Start - 1
						updatedquery = append(updatedquery, Range{Start: v.End + 1, End: end})
					} else {
						updatedquery = append(updatedquery, Range{Start: start, End: v.Start - 1})
						updatedquery = append(updatedquery, Range{Start: v.End + 1, End: end})
					}
				}
			}
		}
		if hit {
			return nil
		}
	}
	return updatedquery
}

// GetByteRanges gets the individual byte ranges from a single/ multi range request
func GetByteRanges(byteRange string) Ranges {
	if byteRange == "" {
		log.Error("Got an empty byte range", log.Pairs{"byteRange": ""})
		return nil
	}
	// byteRange currently has something like this bytes=0-50, 100-150
	// we do some processing to get rid of the text part
	numeric := strings.Split(byteRange, "=")
	if numeric == nil || len(numeric) < 2 {
		log.Error("Couldn't parse the byteranges for a valid range", log.Pairs{"byteRange string": byteRange})
		return nil
	}
	byteRange = numeric[1]
	// example: curl http://www.example.com -i -H "Range: bytes=0-50, 100-150"
	r := strings.Split(byteRange, ",")
	ranges := make(Ranges, len(r))
	for k, v := range r {
		v = strings.TrimSpace(v)
		r2 := strings.Split(v, "-")
		if r2 == nil || len(r2) != 2 {
			log.Error("Couldn't convert byte range to valid indices", log.Pairs{"byteRange": byteRange})
			return nil
		}
		start, err := strconv.Atoi(r2[0])
		if err != nil {
			log.Error("Couldn't get a range", log.Pairs{"start": start})
			return nil
		}
		end, err := strconv.Atoi(r2[1])
		if err != nil {
			log.Error("Couldn't get a range", log.Pairs{"end": end})
			return nil
		}
		ranges[k].Start = start
		ranges[k].End = end
	}
	return ranges
}
