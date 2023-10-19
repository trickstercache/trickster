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
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// ResultHeaderParts defines the components for building the Trickster Result Header
type ResultHeaderParts struct {
	Engine            string
	Status            string
	Fetched           timeseries.ExtentList
	FastForwardStatus string
}

func (p ResultHeaderParts) String() string {
	var sb strings.Builder
	sb.WriteString("engine=" + p.Engine)
	if p.Status != "" {
		sb.WriteString("; status=" + p.Status)
	}
	if len(p.Fetched) > 0 {
		sb.WriteString("; fetched=[" + p.Fetched.String() + "]")
	}
	if p.FastForwardStatus != "" {
		sb.WriteString("; ffstatus=" + p.FastForwardStatus)
	}
	return sb.String()
}

// SetResultsHeader adds a response header summarizing Trickster's handling of the HTTP request
func SetResultsHeader(headers http.Header, engine, status, ffstatus string, fetched timeseries.ExtentList) {
	if headers == nil || engine == "" {
		return
	}
	p := ResultHeaderParts{Engine: engine, Status: status, Fetched: fetched, FastForwardStatus: ffstatus}
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
		r1.Status = "phit"
	}

	if r1.FastForwardStatus == "" {
		r1.FastForwardStatus = r2.FastForwardStatus
	} else if r1.FastForwardStatus != r2.FastForwardStatus {
		r1.FastForwardStatus = "phit"
	}

	if len(r1.Fetched) == 0 {
		r1.Fetched = r2.Fetched
	} else {
		r1.Fetched = append(r1.Fetched, r2.Fetched...)
		r1.Fetched = r1.Fetched.Compress(0)
	}

	return r1.String()

}

func parseResultHeaderVals(h string) ResultHeaderParts {

	r := ResultHeaderParts{}
	parts := strings.Split(h, "; ")
	for _, part := range parts {
		if i := strings.Index(part, "="); i > 0 && i < len(part)-1 {
			key := part[0:i]
			val := part[i+1:]

			switch key {
			case "engine":
				if val != "" {
					r.Engine = val
				}
			case "status":
				if val != "" {
					r.Status = val
				}
			case "ffstatus":
				if val != "" {
					r.FastForwardStatus = val
				}
			case "fetched":
				val = strings.ReplaceAll(strings.ReplaceAll(val, "[", ""), "]", "")
				fparts := strings.Split(val, ";")
				el := make(timeseries.ExtentList, 0, len(fparts))
				for _, fpart := range fparts {
					if i = strings.Index(fpart, "-"); i > 0 && i < len(fpart)-1 {

						start, err := strconv.ParseInt(fpart[0:i], 10, 64)
						if err != nil {
							continue
						}
						end, err := strconv.ParseInt(fpart[i+1:], 10, 64)
						if err != nil {
							continue
						}
						el = append(el, timeseries.Extent{
							Start: time.Unix(0, start*1000000),
							End:   time.Unix(0, end*1000000),
						},
						)
					}
				}
				r.Fetched = el
			}
		}
	}

	return r

}
