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
	"fmt"
	"github.com/Comcast/trickster/internal/util/log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:generate msgp

// Range represents the start and end for a byte range object
type Range struct {
	Start int `msg:"start"`
	End   int `msg:"end"`
}

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
	hit := false
	sort.SliceStable(r, func(i, j int) bool {
		return r[i].Start < r[j].Start
	})

	updatedquery := make([]Range, (len([]Range(byteRange))))
	for k, val := range byteRange {
		start := val.Start
		end := val.End

		if (r[0].Start > end) ||
			(r[len(r)-1].End < start) {
			//New retrieve from upstream
			r = append(r, Range{Start: start, End: end})
			d.Ranges = r
			if d.UpdatedQueryRange == nil {
				d.UpdatedQueryRange = make(Ranges, len([]Range(byteRange)))
			}
			d.UpdatedQueryRange[k].Start = start
			d.UpdatedQueryRange[k].End = end
			hit = false
		} else {
			for _, v := range r {
				if start > v.Start && end < v.End {
					// Just return the intermediate bytes from the cache, since we have everything in the cache
					hit = true
				} else if start > v.Start && end > v.End {
					start = v.End
					hit = false
				} else if start < v.Start && end < v.Start {
					// Just return the same start and end, since we have a full cache miss
					hit = false
				} else if start < v.Start && end < v.End {
					end = v.Start
					hit = false
				}
			}
		}
		if hit {
			return nil
		}
		updatedquery[k].Start = start
		updatedquery[k].End = end
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

// CachingPolicy ...
type CachingPolicy struct {
	IsFresh               bool      `msg:"is_fresh"`
	NoCache               bool      `msg:"nocache"`
	NoTransform           bool      `msg:"notransform"`
	FreshnessLifetime     int       `msg:"freshness_lifetime"`
	CanRevalidate         bool      `msg:"can_revalidate"`
	MustRevalidate        bool      `msg:"must_revalidate"`
	LastModified          time.Time `msg:"last_modified"`
	Expires               time.Time `msg:"expires"`
	Date                  time.Time `msg:"date"`
	LocalDate             time.Time `msg:"local_date"`
	ETag                  string    `msg:"etag"`
	IfNoneMatchValue      string    `msg:"if_none_match_value"`
	IfMatchValue          string    `msg:"if_match_value"`
	IfModifiedSinceTime   time.Time `msg:"if_modified_since_time"`
	IfUnmodifiedSinceTime time.Time `msg:"if_unmodified_since_time"`
}

func (c *CachingPolicy) String() string {
	return fmt.Sprintf(`{ "is_fresh":%t, "no_cache":%t, "no_transform":%t, "freshness_lifetime":%d, "can_revalidate":%t, "must_revalidate":%t,`+
		` "last_modified":%d, "expires":%d, "date":%d, "local_date":%d, "etag":"%s", "if_none_match":"%s", "if_match":"%s",`+
		` "if_modified_since":%d, "if_unmodified_since":%d }`,
		c.IsFresh, c.NoCache, c.NoTransform, c.FreshnessLifetime, c.CanRevalidate, c.MustRevalidate, c.LastModified.Unix(), c.Expires.Unix(), c.Date.Unix(), c.LocalDate.Unix(),
		c.ETag, c.IfNoneMatchValue, c.IfMatchValue, c.IfModifiedSinceTime.Unix(), c.IfUnmodifiedSinceTime.Unix())
}

// DocumentFromHTTPResponse returns an HTTPDocument from the provided HTTP Response and Body
func DocumentFromHTTPResponse(resp *http.Response, body []byte, cp *CachingPolicy) *HTTPDocument {
	d := &HTTPDocument{}
	d.Headers = resp.Header
	d.StatusCode = resp.StatusCode
	d.Status = resp.Status
	d.Body = body
	d.CachingPolicy = cp
	return d
}
