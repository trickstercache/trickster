/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package types

import "strconv"

// TracerType enumerates the methodologies for maintaining time series cache data
type TracerType int

const (
	// TracerTypeNone indicates a No-op tracer
	TracerTypeNone = TracerType(iota)
	// TracerTypeStdout indicates the stdout tracing
	TracerTypeStdout
	// TracerTypeJaeger indicates Jaeger tracing
	TracerTypeJaeger
	// TracerTypeZipkin indicates Zipkin tracing
	TracerTypeZipkin
)

// Names is a map of cache types keyed by name
var Names = map[string]TracerType{
	"none":   TracerTypeNone,
	"stdout": TracerTypeStdout,
	"jaeger": TracerTypeJaeger,
	"zipkin": TracerTypeZipkin,
}

// Values is a map of cache types keyed by internal id
var Values = make(map[TracerType]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
}

func (t TracerType) String() string {
	if v, ok := Values[t]; ok {
		return v
	}
	return strconv.Itoa(int(t))
}
