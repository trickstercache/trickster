/*
 * Copyright 2026 The Trickster Authors
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

package victoriametrics

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
)

func TestPrepareGraphite(t *testing.T) {
	c := newTestClient(t, "http://vm.example:8428", nil)
	past := time.Now().Add(-6 * time.Hour)
	render := url.Values{"target": {"a.b"}, "from": {secs(past.Add(-time.Hour))}, "until": {secs(past)}}
	tests := []struct {
		name, method, path, contentType, rawQuery string
		values                                    url.Values
		want                                      bool
	}{
		{"render get", http.MethodGet, "/render", "", "", render, true},
		{"render post", http.MethodPost, "/render", "application/x-www-form-urlencoded", "", render, true},
		{"render relative", http.MethodGet, "/render", "", "", url.Values{"target": {"a"}, "from": {"-1h"}, "until": {secs(past)}}, false},
		{"render open range", http.MethodGet, "/render", "", "", url.Values{"target": {"a"}, "from": {secs(past)}}, false},
		{"render repeated until", http.MethodGet, "/render", "", "until=" + secs(past), render, false},
		{"render live", http.MethodGet, "/render", "", "", url.Values{"target": {"a"}, "from": {secs(past)}, "until": {secs(time.Now())}}, false},
		{"find", http.MethodGet, "/metrics/find", "", "", url.Values{"query": {"a.*"}}, true},
		{"find relative", http.MethodGet, "/metrics/find", "", "", url.Values{"query": {"a.*"}, "from": {"-1d"}}, false},
		{"delete", http.MethodDelete, "/render", "", "", render, false},
		{"json post", http.MethodPost, "/render", "application/json", "", render, false},
		{"post conflict", http.MethodPost, "/render", "application/x-www-form-urlencoded", "target=b", render, false},
		{"bad query", http.MethodGet, "/metrics/find", "", "%zz", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := test.path
			var body *strings.Reader
			if test.method == http.MethodPost {
				body = strings.NewReader(test.values.Encode())
				if test.rawQuery != "" {
					target += "?" + test.rawQuery
				}
			} else {
				q := test.values.Encode()
				if test.rawQuery != "" {
					q = strings.TrimPrefix(q+"&"+test.rawQuery, "&")
				}
				target += "?" + q
				body = strings.NewReader("")
			}
			r := httptest.NewRequest(test.method, target, body)
			if test.contentType != "" {
				r.Header.Set("Content-Type", test.contentType)
			}
			if got := c.prepareGraphite(r); got != test.want {
				t.Errorf("got %v want %v", got, test.want)
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/render?"+render.Encode(), nil)
	pc := po.New()
	pc.RequestParams = map[string]string{"extra_label": "a=b"}
	r = request.SetResources(r, request.NewResources(c.Configuration(), pc, nil, nil, c, nil))
	if c.prepareGraphite(r) {
		t.Error("a path that rewrites request parameters was cached")
	}
	r = httptest.NewRequest(http.MethodPost, "/render", strings.NewReader("%zz"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.prepareGraphite(r) {
		t.Error("an unparsable form was cached")
	}
	if graphiteSuffix("/graphite/render") != "/render" || graphiteSuffix("/graphiteish") != "/graphiteish" {
		t.Error("unexpected /graphite alias handling")
	}
}
