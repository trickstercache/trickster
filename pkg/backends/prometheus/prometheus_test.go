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
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	cr "github.com/trickstercache/trickster/v2/pkg/cache/registry"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	pe "github.com/trickstercache/trickster/v2/pkg/proxy/errors"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestPrometheusClientInterfacing(t *testing.T) {
	// this test ensures the client will properly conform to the
	// Backend and TimeseriesBackend interfaces

	c, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Error(err)
	}
	var oc backends.Backend = c
	var tc backends.TimeseriesBackend = c.(*Client)

	if oc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", oc.Name())
	}

	if tc.Name() != "test" {
		t.Errorf("expected %s got %s", "test", tc.Name())
	}
}

func TestNewClient(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))

	conf, err := config.Load([]string{
		"-origin-url", "http://1",
		"-provider", "test",
	})
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	caches := cr.LoadCachesFromConfig(conf)
	defer cr.CloseCaches(caches)
	cache, ok := caches["default"]
	if !ok {
		t.Errorf("Could not find default configuration")
	}

	o := &bo.Options{Provider: "TEST_CLIENT"}
	c, err := NewClient("default", o, nil, cache, nil, nil)
	if err != nil {
		t.Error(err)
	}

	if c.Name() != "default" {
		t.Errorf("expected %s got %s", "default", c.Name())
	}

	if c.Cache().Configuration().Provider != "memory" {
		t.Errorf("expected %s got %s", "memory", c.Cache().Configuration().Provider)
	}

	if c.Configuration().Provider != "TEST_CLIENT" {
		t.Errorf("expected %s got %s", "TEST_CLIENT", c.Configuration().Provider)
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		output string
		hasErr bool
	}{
		{"RFC3339", "2018-04-07T05:08:53.200Z", "2018-04-07 05:08:53.2 +0000 UTC", false},
		{"unix integer", "1523077733", "2018-04-07 05:08:53 +0000 UTC", false},
		{"unix float", "1523077733.2", "2018-04-07 05:08:53.2 +0000 UTC", false},
		{"invalid string", "a", "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := parseTime(test.input)
			if test.hasErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s := out.UTC().String(); s != test.output {
				t.Errorf("expected %s got %s", test.output, s)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
		hasErr   bool
	}{
		{"integer seconds", "15", 15 * time.Second, false},
		{"float seconds", "1.5", 1 * time.Second, false},
		{"duration string", "1m", 60 * time.Second, false},
		{"invalid", "x", 0, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d, err := parseDuration(test.input)
			if test.hasErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d != test.expected {
				t.Errorf("expected %s got %s", test.expected, d)
			}
		})
	}
}

func TestParseTimeRangeQuery(t *testing.T) {
	logger.SetLogger(testLogger)
	qp := url.Values(map[string][]string{
		"query": {`up-` + timeseries.FastForwardUserDisableFlag + " " +
			timeseries.BackfillToleranceFlag + "30a"},
		"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
		"end":   {strconv.Itoa(int(time.Now().Unix()))},
		"step":  {"15"},
	})

	u := &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: qp.Encode(),
	}

	req := &http.Request{URL: u}
	o := bo.New()
	o.Prometheus = &po.Options{Labels: map[string]string{"test": "trickster"}}
	rsc := request.NewResources(o, nil, nil, nil, nil, nil)
	req = request.SetResources(req, rsc)

	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	} else {
		if int(res.Step.Seconds()) != 15 {
			t.Errorf("expected 15 got %d", int(res.Step.Seconds()))
		}

		if int(res.Extent.End.Sub(res.Extent.Start).Hours()) != 6 {
			t.Errorf("expected 6 got %d", int(res.Extent.End.Sub(res.Extent.Start).Hours()))
		}
	}

	b := bytes.NewBufferString(qp.Encode())
	u.RawQuery = ""
	req, _ = http.NewRequest(http.MethodPost, u.String(), b)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, _, _, err = client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
	}
}

func TestParseTimeRangeQueryErrors(t *testing.T) {
	validStart := strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))
	validEnd := strconv.Itoa(int(time.Now().Unix()))

	tests := []struct {
		name     string
		params   url.Values
		expected string
	}{
		{
			name:     "missing query",
			params:   url.Values{"start": {validStart}, "end": {validEnd}, "step": {"15"}},
			expected: pe.MissingURLParam(upQuery).Error(),
		},
		{
			name:     "missing start",
			params:   url.Values{"query": {"up"}, "end": {validEnd}, "step": {"15"}},
			expected: `missing URL parameter: [start]`,
		},
		{
			name:     "missing end",
			params:   url.Values{"query": {"up"}, "start": {validStart}, "step": {"15"}},
			expected: `missing URL parameter: [end]`,
		},
		{
			name:     "missing step",
			params:   url.Values{"query": {"up"}, "start": {validStart}, "end": {validEnd}},
			expected: `missing URL parameter: [step]`,
		},
		{
			name:     "bad start time",
			params:   url.Values{"query": {"up"}, "start": {"red"}, "end": {validEnd}, "step": {"15"}},
			expected: `cannot parse "red" to a valid timestamp`,
		},
		{
			name:     "bad end time",
			params:   url.Values{"query": {"up"}, "start": {validStart}, "end": {"blue"}, "step": {"15"}},
			expected: `cannot parse "blue" to a valid timestamp`,
		},
		{
			name:     "bad step duration",
			params:   url.Values{"query": {"up"}, "start": {validStart}, "end": {validEnd}, "step": {"x"}},
			expected: `duration literal x: expected value of at least length 2 at position 0`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := &http.Request{URL: &url.URL{
				Scheme:   "https",
				Host:     "blah.com",
				Path:     "/",
				RawQuery: test.params.Encode(),
			}}
			client := &Client{}
			_, _, _, err := client.ParseTimeRangeQuery(req)
			if err == nil {
				t.Fatalf("expected error %q, got nil", test.expected)
			}
			if err.Error() != test.expected {
				t.Errorf("expected %q got %q", test.expected, err.Error())
			}
		})
	}
}

func TestParseTimeRangeQueryWithOffset(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{
			"query": {`up and has offset `},
			"start": {strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))},
			"end":   {strconv.Itoa(int(time.Now().Unix()))},
			"step":  {"15"},
		}).Encode(),
	}}
	client := &Client{}
	res, _, _, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		t.Error(err)
		return
	}

	if !res.IsOffset {
		t.Errorf("expected true got %t", res.IsOffset)
	}
}

func TestParseVectorQuery(t *testing.T) {
	logger.SetLogger(testLogger)
	validTime := strconv.Itoa(int(time.Now().Add(time.Duration(-6) * time.Hour).Unix()))
	rounder := time.Second * 15

	tests := []struct {
		name   string
		params url.Values
		hasErr bool
	}{
		{
			name:   "valid with time",
			params: url.Values{"query": {"up and has offset "}, "time": {validTime}},
		},
		{
			name:   "missing query",
			params: url.Values{"time": {validTime}},
			hasErr: true,
		},
		{
			name:   "bad time",
			params: url.Values{"query": {"up and has offset "}, "time": {"a"}},
			hasErr: true,
		},
		{
			name:   "no time uses now",
			params: url.Values{"query": {"up and has offset "}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := &http.Request{URL: &url.URL{
				Scheme:   "https",
				Host:     "blah.com",
				Path:     "/",
				RawQuery: test.params.Encode(),
			}}
			_, err := parseVectorQuery(req, rounder)
			if test.hasErr && err == nil {
				t.Error("expected error")
			}
			if !test.hasErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
