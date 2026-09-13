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

package rule

import (
	"net/http"
	"strings"

	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/parts"
)

type (
	inputType      string
	extractionFunc func(*http.Request, string) string
)

// the header sources are given a canonical header name at parse time, which
// is what the parts lookups expect
var sourceExtractionFuncs = map[inputType]extractionFunc{
	ro.SourceMethod:      func(r *http.Request, _ string) string { return parts.Method(r) },
	ro.SourceURL:         func(r *http.Request, _ string) string { return parts.URL(r) },
	ro.SourceURLNoParams: func(r *http.Request, _ string) string { return parts.URLNoParams(r) },
	ro.SourceScheme:      func(r *http.Request, _ string) string { return parts.Scheme(r) },
	ro.SourceHost:        func(r *http.Request, _ string) string { return parts.Host(r) },
	ro.SourceHostname:    func(r *http.Request, _ string) string { return parts.Hostname(r) },
	ro.SourcePort:        func(r *http.Request, _ string) string { return parts.Port(r) },
	ro.SourcePath:        func(r *http.Request, _ string) string { return parts.Path(r) },
	ro.SourceParams:      func(r *http.Request, _ string) string { return parts.RawQuery(r) },
	ro.SourceParam:       extractParamFromSource,
	ro.SourceHeader:      parts.Header,
	ro.SourceHasParam:    extractParamPresenceFromSource,
	ro.SourceHasHeader:   extractHeaderPresenceFromSource,
}

func isValidSourceName(source string) (extractionFunc, bool) {
	f, ok := sourceExtractionFuncs[inputType(source)]
	return f, ok
}

func isHeaderSource(source string) bool {
	return source == ro.SourceHeader || source == ro.SourceHasHeader
}

func extractParamFromSource(r *http.Request, paramName string) string {
	return parts.Param(parts.Query(r), paramName)
}

func extractHeaderPresenceFromSource(r *http.Request, headerName string) string {
	return btos(parts.HasHeader(r, headerName), false)
}

func extractParamPresenceFromSource(r *http.Request, paramName string) string {
	return btos(parts.HasParam(parts.Query(r), paramName), false)
}

// assumes delimiter is not empty string, and part is >= 0
func extractSourcePart(input, delimiter string, part int) string {
	if input == "" || len(delimiter) > len(input) {
		return ""
	}
	parts := strings.Split(input, delimiter)
	if len(parts) <= part {
		return ""
	}
	return parts[part]
}
