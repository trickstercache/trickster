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

	InfluxqlGet  = isInfluxql
	InfluxqlPost = isInfluxql + isInfluxqlPost

	FluxJsonJson = isFlux + isFluxInputJSON + isFluxOutputJSON
	FluxJsonCsv  = isFlux + isFluxInputJSON

	FluxRawJson = isFlux + isFluxOutputJSON
	FluxRawCsv  = isFlux
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

func (f Format) IsPost() bool {
	return f&isFlux == isFlux || f&isInfluxqlPost == isInfluxqlPost
}

func Detect(r *http.Request) Format {
	if r.Method == http.MethodGet {
		return InfluxqlGet
	}
	if r.Method != http.MethodPost {
		return Unknown
	}
	isJsonOut := headers.AcceptsJSON(r)
	switch {
	case headers.ProvidesURLEncodedForm(r):
		return InfluxqlPost
	case headers.ProvidesJSON(r):
		if isJsonOut {
			return FluxJsonJson
		}
		return FluxJsonCsv
	case ProvidesFlux(r):
		if isJsonOut {
			return FluxRawJson
		}
		return FluxRawCsv
	}
	return Unknown
}

func ProvidesFlux(r *http.Request) bool {
	return headers.ProvidesContentType(r, headers.ValueApplicationFlux)
}
