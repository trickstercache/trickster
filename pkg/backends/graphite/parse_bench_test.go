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

package graphite

import (
	"fmt"
	"net/http"
	"testing"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func benchmarkParse(b *testing.B, extraPaths int) {
	o := bo.New()
	o.Graphite = gro.New()
	o.Graphite.OriginUsername = "metrics"
	o.Graphite.OriginPassword = "s3cret"
	for i := range extraPaths {
		o.Paths = append(o.Paths, &po.Options{
			Path: fmt.Sprintf("/custom/%d", i), Methods: methods.GetAndPost(),
			MatchType: matching.PathMatchTypeExact, MatchTypeName: matching.PathMatchNameExact,
			RequestHeaders: map[string]string{"X-Custom": fmt.Sprintf("%d", i)},
		})
	}
	c := newTestClient(b, o)
	o.Paths = c.DefaultPathConfigs(o).Overlay(o.Paths)
	pc := o.Paths.Match(http.MethodGet, renderPath)
	r := getReq("target=a.b&from=-6h&until=now&format=json")
	r = request.SetResources(r, request.NewResources(nil, pc, nil, nil, nil, nil))
	if _, _, _, err := c.ParseTimeRangeQuery(r); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, _, _, err := c.ParseTimeRangeQuery(r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseTimeRangeQueryDefaultPaths(b *testing.B) {
	benchmarkParse(b, 0)
}

func BenchmarkParseTimeRangeQueryLargePathList(b *testing.B) {
	benchmarkParse(b, 1000)
}
