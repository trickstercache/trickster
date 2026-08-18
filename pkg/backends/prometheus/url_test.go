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

package prometheus

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetExtent(t *testing.T) {
	start := time.Now().Add(time.Duration(-6) * time.Hour)
	end := time.Now()

	startSecs := fmt.Sprintf("%d", start.Unix())
	endSecs := fmt.Sprintf("%d", end.Unix())

	expected := "end=" + endSecs + "&q=up&start=" + startSecs

	conf, err := config.Load([]string{
		"-origin-url", "none:9090", "-provider",
		providers.Prometheus, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	pc := client.(*Client)

	u := &url.URL{RawQuery: "q=up"}

	r, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	e := &timeseries.Extent{Start: start, End: end}
	pc.SetExtent(r, nil, e)

	if expected != r.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, r.URL.RawQuery)
	}

	u2 := urls.Clone(u)
	u2.RawQuery = ""

	b := bytes.NewBufferString(expected)
	r, _ = http.NewRequest(http.MethodPost, u2.String(), b)

	pc.SetExtent(r, nil, e)
	if int(r.ContentLength) != len(expected) {
		b, _ := io.ReadAll(r.Body)
		t.Errorf("expected %d got %d / %d", len(expected), r.ContentLength, len(b))
	}
}

func TestFastForwardURL(t *testing.T) {
	expected := "q=up&time=1"

	conf, err := config.Load([]string{
		"-origin-url", "none:9090", "-provider",
		providers.Prometheus, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}

	pc := client.(*Client)

	u := &url.URL{Path: "/query_range", RawQuery: "q=up&start=1&end=1&step=1"}
	r, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	r = request.SetResources(r, &request.Resources{})

	r2, err := pc.FastForwardRequest(r)
	if err != nil {
		t.Error(err)
	}

	if expected != r2.URL.RawQuery {
		t.Errorf("\nexpected [%s]\ngot [%s]", expected, r2.URL.RawQuery)
	}

	r2.URL.RawQuery = ""
	b := bytes.NewBufferString(expected)
	r, _ = http.NewRequest(http.MethodPost, r2.URL.String(), b)
	r = request.SetResources(r, &request.Resources{})

	_, err = pc.FastForwardRequest(r)
	if err != nil {
		t.Error(err)
	}
}

func TestFastForwardRequestPromotesEndToTime(t *testing.T) {
	conf, err := config.Load([]string{
		"-origin-url", "none:9090", "-provider",
		providers.Prometheus, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	client, err := NewClient("default", conf.Backends["default"], nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := client.(*Client)

	tests := []struct {
		name        string
		method      string
		target      string
		body        string
		contentType string
		wantTime    string
	}{
		{
			name:     "GET Unix timestamp",
			method:   http.MethodGet,
			target:   "/api/v1/query_range?query=up&start=1&end=1785801280&step=60",
			wantTime: "1785801280",
		},
		{
			name:     "GET fractional Unix timestamp",
			method:   http.MethodGet,
			target:   "/api/v1/query_range?query=up&start=1&end=1785801280.25&step=60",
			wantTime: "1785801280.25",
		},
		{
			name:     "GET RFC3339 timestamp",
			method:   http.MethodGet,
			target:   "/api/v1/query_range?query=up&start=1&end=2026-08-03T23%3A54%3A40Z&step=60",
			wantTime: "2026-08-03T23:54:40Z",
		},
		{
			name:        "POST form timestamp",
			method:      http.MethodPost,
			target:      "/api/v1/query_range?stats=all",
			body:        "query=up&start=1&end=1785801280.5&step=60",
			contentType: "application/x-www-form-urlencoded",
			wantTime:    "1785801280.5",
		},
		{
			name:     "range end replaces irrelevant time",
			method:   http.MethodGet,
			target:   "/api/v1/query_range?query=up&start=1&end=1785801280&step=60&time=old",
			wantTime: "1785801280",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body io.Reader
			if test.body != "" {
				body = strings.NewReader(test.body)
			}
			r, err := http.NewRequest(test.method, test.target, body)
			if err != nil {
				t.Fatal(err)
			}
			if test.contentType != "" {
				r.Header.Set("Content-Type", test.contentType)
			}
			r = request.SetResources(r, &request.Resources{})

			got, err := pc.FastForwardRequest(r)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL.Path != "/api/v1/query" {
				t.Fatalf("path got %q want %q", got.URL.Path, "/api/v1/query")
			}
			values := got.URL.Query()
			if gotTime := values.Get(upTime); gotTime != test.wantTime {
				t.Errorf("time got %q want %q", gotTime, test.wantTime)
			}
			for _, name := range []string{upStart, upEnd, upStep} {
				if values.Has(name) {
					t.Errorf("unexpected range parameter %q=%q", name, values.Get(name))
				}
			}
			if got.Method == http.MethodPost {
				b, err := io.ReadAll(got.Body)
				if err != nil {
					t.Fatal(err)
				}
				form, err := url.ParseQuery(string(b))
				if err != nil {
					t.Fatal(err)
				}
				if gotTime := form.Get(upTime); gotTime != test.wantTime {
					t.Errorf("POST body time got %q want %q", gotTime, test.wantTime)
				}
			}
		})
	}
}

func TestFastForwardRequestEdgeCases(t *testing.T) {
	conf, err := config.Load([]string{
		"-origin-url", "none:9090", "-provider",
		providers.Prometheus, "-log-level", "debug",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	o := conf.Backends["default"]
	client, err := NewClient("default", o, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := client.(*Client)

	tests := []struct {
		name         string
		path         string
		expectedPath string
	}{
		{
			name:         "query_range suffix stripped",
			path:         "/api/v1/query_range",
			expectedPath: "/api/v1/query",
		},
		{
			name:         "root query_range stripped",
			path:         "/query_range",
			expectedPath: "/query",
		},
		{
			name:         "no suffix unchanged",
			path:         "/api/v1/query",
			expectedPath: "/api/v1/query",
		},
		{
			name:         "other path unchanged",
			path:         "/api/v1/labels",
			expectedPath: "/api/v1/labels",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodGet, test.path+"?q=up", nil)
			r = request.SetResources(r, &request.Resources{})
			r2, err := pc.FastForwardRequest(r)
			if err != nil {
				t.Fatal(err)
			}
			if r2.URL.Path != test.expectedPath {
				t.Errorf("path: expected %q got %q", test.expectedPath, r2.URL.Path)
			}
		})
	}
}
