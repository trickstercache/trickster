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

// package iofmt assists with managing supported input and output formats
// across the various versions of InfluxDB
package iofmt

import (
	"errors"
	"net/http"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

type Format byte

const (
	Unknown Format = 0

	isInfluxql       Format = 1 << iota
	isInfluxqlPost          // 2
	isFlux                  // 4
	isFluxInputJSON         // 8
	isFluxOutputJSON        // 16
	isV3SQL                 // 32
	isV3InfluxQL            // 64

	InfluxqlGet  = isInfluxql
	InfluxqlPost = isInfluxql + isInfluxqlPost

	FluxJSONJSON = isFlux + isFluxInputJSON + isFluxOutputJSON
	FluxJSONCsv  = isFlux + isFluxInputJSON

	FluxRawJSON = isFlux + isFluxOutputJSON
	FluxRawCsv  = isFlux

	V3SQL      = isV3SQL
	V3InfluxQL = isV3InfluxQL
)

// V3 output format constants stored in RequestOptions.OutputFormat
const (
	V3OutputJSON  byte = 32
	V3OutputJSONL byte = 33
	V3OutputCSV   byte = 34
)

var ErrSupportedQueryLanguage = errors.New("unsupported query language")

func (f Format) IsInfluxQL() bool {
	return f&isInfluxql == isInfluxql
}

func (f Format) IsFlux() bool {
	return f&isFlux == isFlux
}

func (f Format) IsFluxInputJSON() bool {
	return f&isFluxInputJSON == isFluxInputJSON
}

func (f Format) IsFluxOutputJSON() bool {
	return f&isFluxOutputJSON == isFluxOutputJSON
}

func (f Format) IsV3SQL() bool {
	return f&isV3SQL == isV3SQL
}

func (f Format) IsV3InfluxQL() bool {
	return f&isV3InfluxQL == isV3InfluxQL
}

func (f Format) IsV3() bool {
	return f.IsV3SQL() || f.IsV3InfluxQL()
}

func (f Format) IsPost() bool {
	return f&isFlux == isFlux || f&isInfluxqlPost == isInfluxqlPost
}

// V3OutputFormat returns the v3 output format byte from the request's format param.
func V3OutputFormat(r *http.Request) byte {
	switch strings.ToLower(r.URL.Query().Get("format")) {
	case "jsonl":
		return V3OutputJSONL
	case "csv":
		return V3OutputCSV
	default:
		return V3OutputJSON
	}
}

func Detect(r *http.Request) Format {
	if r.URL != nil {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/api/v3/query_sql"):
			return V3SQL
		case strings.HasSuffix(p, "/api/v3/query_influxql"):
			return V3InfluxQL
		}
	}
	if r.Method == http.MethodGet {
		return InfluxqlGet
	}
	if r.Method != http.MethodPost {
		return Unknown
	}
	jo := headers.AcceptsJSON(r)
	switch {
	case headers.ProvidesURLEncodedForm(r):
		return InfluxqlPost
	case headers.ProvidesJSON(r):
		if jo {
			return FluxJSONJSON
		}
		return FluxJSONCsv
	case ProvidesFlux(r):
		if jo {
			return FluxRawJSON
		}
		return FluxRawCsv
	}
	return Unknown
}

func ProvidesFlux(r *http.Request) bool {
	return headers.ProvidesContentType(r, headers.ValueApplicationFlux)
}
