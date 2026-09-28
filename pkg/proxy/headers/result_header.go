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

package headers

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/status"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// ResultHeaderParts defines the components for building the Trickster Result Header
type ResultHeaderParts struct {
	Engine            string
	Status            string
	Fetched           timeseries.ExtentList
	FailedFetch       timeseries.ExtentList
	FastForwardStatus string
	PartialBuckets    []PartialBucketResult
}

// PartialBucketResult reports one partial bucket fetch: its half-open fetched range, the edge of the
// request's range it sits on, and its object proxy cache lookup status
type PartialBucketResult struct {
	Extent timeseries.Extent
	Edge   timeseries.BucketEdge
	Status string
}

const (
	partialBucketSeparator = ";"
	partialBucketFieldSep  = ":"
)

func writePartialBuckets(sb *strings.Builder, pbs []PartialBucketResult) {
	for i, pb := range pbs {
		if i > 0 {
			sb.WriteString(partialBucketSeparator)
		}
		sb.WriteString(strconv.FormatInt(pb.Extent.Start.UnixMilli(), 10))
		sb.WriteByte('-')
		sb.WriteString(strconv.FormatInt(pb.Extent.End.UnixMilli(), 10))
		sb.WriteString(partialBucketFieldSep)
		sb.WriteString(pb.Edge.String())
		sb.WriteString(partialBucketFieldSep)
		sb.WriteString(pb.Status)
	}
}

func parsePartialBuckets(val string) []PartialBucketResult {
	val = strings.TrimSuffix(strings.TrimPrefix(val, "["), "]")
	out := make([]PartialBucketResult, 0, strings.Count(val, partialBucketSeparator)+1)
	for entry := range strings.SplitSeq(val, partialBucketSeparator) {
		rng, rest, ok := strings.Cut(entry, partialBucketFieldSep)
		if !ok {
			continue
		}
		edge, st, ok := strings.Cut(rest, partialBucketFieldSep)
		if !ok || st == "" {
			continue
		}
		ext, ok := parseMillisRange(rng)
		if !ok {
			continue
		}
		be, ok := timeseries.ParseBucketEdge(edge)
		if !ok {
			continue
		}
		out = append(out, PartialBucketResult{Extent: ext, Edge: be, Status: st})
	}
	return out
}

func parseMillisRange(rng string) (timeseries.Extent, bool) {
	if len(rng) < 3 {
		return timeseries.Extent{}, false
	}
	// the separator follows the start's first character, so a minus there is the start's sign
	i := strings.IndexByte(rng[1:], '-') + 1
	if i == 0 {
		return timeseries.Extent{}, false
	}
	start, err := strconv.ParseInt(rng[:i], 10, 64)
	if err != nil {
		return timeseries.Extent{}, false
	}
	end, err := strconv.ParseInt(rng[i+1:], 10, 64)
	if err != nil {
		return timeseries.Extent{}, false
	}
	return timeseries.Extent{Start: time.UnixMilli(start), End: time.UnixMilli(end)}, true
}

func mergePartialBuckets(a, b []PartialBucketResult) []PartialBucketResult {
	if len(a) == 0 {
		return b
	}
	out := a
	for _, pb := range b {
		i := slices.IndexFunc(out, func(x PartialBucketResult) bool {
			return x.Edge == pb.Edge && x.Extent.Start.Equal(pb.Extent.Start) && x.Extent.End.Equal(pb.Extent.End)
		})
		// the same range and edge from two members merges as ffstatus does
		switch {
		case i < 0:
			out = append(out, pb)
		case out[i].Status != pb.Status:
			out[i].Status = status.StatusPartialHit
		}
	}
	return out
}

func (p ResultHeaderParts) String() string {
	var sb strings.Builder
	sb.WriteString("engine=")
	sb.WriteString(p.Engine)
	if p.Status != "" {
		sb.WriteString("; status=")
		sb.WriteString(p.Status)
	}
	if len(p.Fetched) > 0 {
		sb.WriteString("; fetched=[")
		sb.WriteString(p.Fetched.String())
		sb.WriteString("]")
	}
	if p.FastForwardStatus != "" {
		sb.WriteString("; ffstatus=")
		sb.WriteString(p.FastForwardStatus)
	}
	if len(p.PartialBuckets) > 0 {
		sb.WriteString("; " + keys.PartialBuckets + "=[")
		writePartialBuckets(&sb, p.PartialBuckets)
		sb.WriteString("]")
	}
	if len(p.FailedFetch) > 0 {
		sb.WriteString("; failed=[")
		sb.WriteString(p.FailedFetch.String())
		sb.WriteString("]")
	}
	return sb.String()
}

// SetResultsHeader adds a response header summarizing Trickster's handling of the HTTP request
func SetResultsHeader(headers http.Header, engine, status, ffstatus string, fetched timeseries.ExtentList, failedFetched timeseries.ExtentList) {
	if headers == nil || engine == "" {
		return
	}
	p := ResultHeaderParts{Engine: engine, Status: status, Fetched: fetched, FailedFetch: failedFetched, FastForwardStatus: ffstatus}
	headers.Set(NameTricksterResult, p.String())
}

// MakeResultsHeader returns a header value summarizing Trickster's handling of the HTTP request
func MakeResultsHeader(engine, status, ffstatus string, fetched timeseries.ExtentList) string {
	p := ResultHeaderParts{Engine: engine, Status: status, Fetched: fetched, FastForwardStatus: ffstatus}
	return p.String()
}

// MergeResultHeaderVals merges 2 Trickster Result Headers
func MergeResultHeaderVals(h1, h2 string) string {
	if h1 == "" {
		return h2
	}

	r1 := parseResultHeaderVals(h1)
	r2 := parseResultHeaderVals(h2)

	if r1.Engine == "" {
		r1.Engine = r2.Engine
	}

	if r1.Status == "" {
		r1.Status = r2.Status
	} else if r1.Status != r2.Status {
		r1.Status = status.StatusPartialHit
	}

	if r1.FastForwardStatus == "" {
		r1.FastForwardStatus = r2.FastForwardStatus
	} else if r1.FastForwardStatus != r2.FastForwardStatus {
		r1.FastForwardStatus = status.StatusPartialHit
	}

	if len(r1.Fetched) == 0 {
		r1.Fetched = r2.Fetched
	} else if len(r2.Fetched) > 0 {
		merged := make(timeseries.ExtentList, len(r1.Fetched)+len(r2.Fetched))
		copy(merged, r1.Fetched)
		copy(merged[len(r1.Fetched):], r2.Fetched)
		r1.Fetched = merged.Compress(0)
	}

	r1.PartialBuckets = mergePartialBuckets(r1.PartialBuckets, r2.PartialBuckets)

	if len(r1.FailedFetch) == 0 {
		r1.FailedFetch = r2.FailedFetch
	} else if len(r2.FailedFetch) > 0 {
		merged := make(timeseries.ExtentList, len(r1.FailedFetch)+len(r2.FailedFetch))
		copy(merged, r1.FailedFetch)
		copy(merged[len(r1.FailedFetch):], r2.FailedFetch)
		r1.FailedFetch = merged.Compress(0)
	}

	return r1.String()
}

func parseResultHeaderVals(h string) ResultHeaderParts {
	r := ResultHeaderParts{}
	parts := strings.SplitSeq(h, "; ")
	for part := range parts {
		if i := strings.Index(part, "="); i > 0 && i < len(part)-1 {
			key := part[0:i]
			val := part[i+1:]

			switch key {
			case keys.Engine:
				if val != "" {
					r.Engine = val
				}
			case keys.Status:
				if val != "" {
					r.Status = val
				}
			case keys.FFStatus:
				if val != "" {
					r.FastForwardStatus = val
				}
			case keys.PartialBuckets:
				r.PartialBuckets = parsePartialBuckets(val)
			case keys.Fetched, keys.Failed:
				val = strings.NewReplacer("[", "", "]", "").Replace(val)
				fparts := strings.Split(val, ";")
				el := make(timeseries.ExtentList, len(fparts))
				var k int
				for _, fpart := range fparts {
					if ext, ok := parseMillisRange(fpart); ok {
						el[k] = ext
						k++
					}
				}

				if key == keys.Fetched {
					r.Fetched = el[:k]
				} else {
					r.FailedFetch = el[:k]
				}
			}
		}
	}
	return r
}

// ParseResultHeader returns the structured values in X-Trickster-Result.
func ParseResultHeader(h string) ResultHeaderParts {
	return parseResultHeaderVals(h)
}

// ParseResultEngineStatus extracts only engine and status without extents.
func ParseResultEngineStatus(h string) (engine, status string) {
	for part := range strings.SplitSeq(h, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || value == "" {
			continue
		}
		switch key {
		case keys.Engine:
			engine = value
		case keys.Status:
			status = value
		}
		if engine != "" && status != "" {
			return engine, status
		}
	}
	return engine, status
}
