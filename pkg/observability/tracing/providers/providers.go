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

package providers

import "strconv"

// Provider enumerates the distributed tracing providers
type Provider int

const (
	// None indicates a No-op tracer
	None = Provider(iota)
	// Stdout indicates the stdout tracing
	Stdout
	// Jaeger indicates Jaeger tracing
	Jaeger
	// Zipkin indicates Zipkin tracing
	Zipkin
)

// Names is a map of tracing providers keyed by name
var Names = map[string]Provider{
	"none":   None,
	"stdout": Stdout,
	"jaeger": Jaeger,
	"zipkin": Zipkin,
}

// Values is a map of tracing providers keyed by internal id
var Values = make(map[Provider]string)

func init() {
	for k, v := range Names {
		Values[v] = k
	}
}

func (p Provider) String() string {
	if v, ok := Values[p]; ok {
		return v
	}
	return strconv.Itoa(int(p))
}
