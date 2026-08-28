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

package parsing

import (
	"net/url"
	"testing"
)

func TestParseRenderParams(t *testing.T) {
	v := url.Values{
		"target": {"a.b", "c.d"}, "from": {"-6h"}, "until": {"now"}, "now": {"1787343600"},
		"tz": {"UTC"}, "format": {"json"}, "jsonp": {"cb"}, "pretty": {"1"}, "maxDataPoints": {"743"},
		"noNullPoints": {"true"}, "xFilesFactor": {"0.5"}, "local": {"1"}, "template[host]": {"web1"},
		"cacheTimeout": {"0"},
	}
	p := ParseRenderParams(v)
	want := RenderParams{Targets: []string{"a.b", "c.d"}, From: "-6h", Until: "now", Now: "1787343600",
		TZ: "UTC", Format: "json", JSONP: "cb", Pretty: true, MaxDataPoints: 743, NoNullPoints: true,
		XFilesFactor: "0.5", Local: true, GraphType: "line", Template: map[string]string{"host": "web1"}}
	if p.Declined != "" || len(p.Targets) != 2 || p.From != want.From || p.Until != want.Until ||
		p.Now != want.Now || p.TZ != want.TZ || p.Format != want.Format || p.JSONP != want.JSONP ||
		!p.Pretty || p.MaxDataPoints != 743 || !p.NoNullPoints || p.XFilesFactor != "0.5" ||
		!p.Local || p.GraphType != "line" || p.Template["host"] != "web1" {
		t.Errorf("got %+v", p)
	}

	tests := []struct {
		name     string
		v        url.Values
		format   string
		declined string
		param    string
		targets  int
	}{
		{"default format is png", url.Values{"target": {"a.b"}}, "png", ReasonNonSeriesFormat, "format", 1},
		{"json", url.Values{"target": {"a.b"}, "format": {"json"}}, "json", "", "", 1},
		{"raw", url.Values{"target": {"a.b"}, "format": {"raw"}}, "raw", "", "", 1},
		{"csv", url.Values{"target": {"a.b"}, "format": {"csv"}}, "csv", "", "", 1},
		{"msgpack", url.Values{"target": {"a.b"}, "format": {"msgpack"}}, "msgpack", "", "", 1},
		{"rawData", url.Values{"target": {"a.b"}, "rawData": {""}}, "raw", "", "", 1},
		{"format overrides rawData", url.Values{"target": {"a.b"}, "rawData": {""}, "format": {"json"}}, "json", "", "", 1},
		{"pickle", url.Values{"target": {"a.b"}, "pickle": {""}}, "pickle", ReasonNonSeriesFormat, "format", 1},
		{"png", url.Values{"target": {"a.b"}, "format": {"png"}}, "png", ReasonNonSeriesFormat, "format", 1},
		{"svg", url.Values{"target": {"a.b"}, "format": {"svg"}}, "svg", ReasonNonSeriesFormat, "format", 1},
		{"dygraph", url.Values{"target": {"a.b"}, "format": {"dygraph"}}, "dygraph", ReasonNonSeriesFormat, "format", 1},
		{"pie", url.Values{"target": {"a.b"}, "format": {"json"}, "graphType": {"pie"}}, "json", ReasonNonSeriesFormat, "graphType", 1},
		{"maxStep", url.Values{"target": {"a.b"}, "format": {"json"}, "maxStep": {"60"}}, "json", ReasonUnknownStep, "maxStep", 1},
		{"target[]", url.Values{"target[]": {"a.b", "c.d"}, "format": {"json"}}, "json", "", "", 2},
		{"no targets", url.Values{"format": {"json"}}, "json", "", "", 0},
		{"maxDataPoints non-digit ignored", url.Values{"target": {"a.b"}, "format": {"json"}, "maxDataPoints": {"abc"}}, "json", "", "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := ParseRenderParams(tc.v)
			if p.Format != tc.format || p.Declined != tc.declined || p.DeclinedParam != tc.param ||
				len(p.Targets) != tc.targets {
				t.Errorf("got %+v", p)
			}
			if tc.name == "maxDataPoints non-digit ignored" && p.MaxDataPoints != 0 {
				t.Errorf("expected maxDataPoints 0, got %d", p.MaxDataPoints)
			}
		})
	}
}

func TestRenderParamClasses(t *testing.T) {
	// every stripped param must be classified as marshal-time
	for _, k := range UpstreamStripParams {
		if RenderParamClasses[k] != ParamMarshal {
			t.Errorf("%s is stripped upstream but not classified ParamMarshal", k)
		}
	}
	// every series format is accepted and the default is not
	for f := range SeriesFormats {
		if !isSeriesFormat(f) {
			t.Errorf("%s should be a series format", f)
		}
	}
	if isSeriesFormat(DefaultFormat) {
		t.Error("the default (image) format must not be a series format")
	}
	if RenderParamClasses["target"] != ParamKey || RenderParamClasses["from"] != ParamExtent ||
		RenderParamClasses["cacheTimeout"] != ParamUpstream || RenderParamClasses["maxStep"] != ParamDecline {
		t.Error("unexpected classification table")
	}
	if _, ok := RenderParamClasses["width"]; ok {
		t.Error("graph options should not be in the table")
	}
}
