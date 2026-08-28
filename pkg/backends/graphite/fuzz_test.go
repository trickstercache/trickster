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
	"net/http"
	"net/url"
	"testing"
)

func FuzzParseTimeRange(f *testing.F) {
	// the contract under fuzz: the parser accepts and hands back a query the
	// delta cache can key on, or declines to the object lane; never a panic
	for _, s := range []string{
		"target=a.b&from=-1h",
		"target=a.b&from=-1h&until=now&format=json&maxDataPoints=743",
		"target=aliasByNode(dev.fast.requests.*.count,%203)&from=-6h",
		"target=a.b&target=c.d&from=-30min&until=-5min",
		"target=movingAverage(a.b,%20'5min')&from=-1h",
		"target=slow.a&target=a.b&from=-1h",
		"target=a.{b,c}&from=-1h",
		"target=constantLine(5)&from=-1h",
		"target=a.b&from=midnight%20yesterday&until=today&tz=America/New_York",
		"target=a.b&from=-120d&until=now",
		"target=a.b&from=now&until=-1h",
		"target=a.b&from=1787349960&until=1787350000",
		// malformed and hostile shapes
		"", "target=", "target=&from=", "from=-1h", "target=(",
		"target=a.b&from=", "target=a.b&from=-", "target=a.b&until=:",
		"target=a.b&maxDataPoints=-1", "target=a.b&maxDataPoints=x",
		"target=a.b&from=99999999999999999999",
		"target=%00&from=-1h", "target=a.b&tz=Nowhere/Nothing",
		"target=a.b&format=", "target=a.b&format=png", "target=a.b&jsonp=%00",
	} {
		f.Add(s)
	}
	c := newTestClient(f, nil)
	f.Fuzz(func(t *testing.T, q string) {
		// built by hand rather than through http.NewRequest, which refuses
		// bytes (a NUL, say) that a query string can still carry
		r := &http.Request{Method: http.MethodGet, Body: http.NoBody, Header: http.Header{},
			URL: &url.URL{Scheme: "http", Host: "graphite", Path: "/render", RawQuery: q}}
		trq, rlo, canOPC, err := c.ParseTimeRangeQuery(r)
		if err != nil {
			// a decline must leave the request servable by the object lane,
			// with no request options the delta lane would act on
			if !canOPC {
				t.Fatalf("%q: declined with %v but canOPC is false", q, err)
			}
			if rlo != nil {
				t.Fatalf("%q: declined with %v but returned request options", q, err)
			}
			return
		}
		if !canOPC {
			t.Fatalf("%q: accepted but canOPC is false", q)
		}
		if trq == nil || rlo == nil {
			t.Fatalf("%q: accepted with trq=%v rlo=%v", q, trq, rlo)
		}
		// an accepted request is about to be cached, so everything the
		// cache key and the extent modeling depend on must be present
		if trq.Step <= 0 {
			t.Fatalf("%q: accepted with step %v", q, trq.Step)
		}
		if trq.Statement == "" {
			t.Fatalf("%q: accepted with an empty statement", q)
		}
		if trq.Extent.End.Before(trq.Extent.Start) {
			t.Fatalf("%q: accepted with an inverted extent %v", q, trq.Extent)
		}
		for _, k := range []string{upTarget, "leaves", "step", "gen"} {
			if _, ok := trq.CacheKeyElements[k]; !ok {
				t.Fatalf("%q: accepted without cache key element %q", q, k)
			}
		}
		// the extent must be aligned to the step it was resolved at, or the
		// delta cache would model ranges the origin never serves
		if trq.Extent.Start.UnixNano()%int64(trq.Step) != 0 ||
			trq.Extent.End.UnixNano()%int64(trq.Step) != 0 {
			t.Fatalf("%q: extent %v is not aligned to step %v", q, trq.Extent, trq.Step)
		}
	})
}
