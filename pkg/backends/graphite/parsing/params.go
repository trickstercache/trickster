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
	"strconv"
	"strings"
)

// ParamClass says what Trickster does with a render URL parameter
type ParamClass int

const (
	// ParamKey parameters change the data the origin returns and are part of
	// the cache key; they are sent upstream unchanged
	ParamKey ParamClass = iota
	// ParamExtent parameters define the time range; the DPC rewrites them
	// per gap extent and they are represented in the key by the extent
	ParamExtent
	// ParamMarshal parameters affect only how the cached DataSet is serialized
	// to this client; they never enter the key and are never sent upstream
	ParamMarshal
	// ParamUpstream parameters are passed to the origin but do not affect
	// series values; not in the key
	ParamUpstream
	// ParamDecline parameters make the request non-accelerable
	ParamDecline
)

// RenderParamClasses is the classification table for graphite-web's render
// parameters (render/views.py parseOptions); unknown parameters are ParamUpstream.
var RenderParamClasses = map[string]ParamClass{
	"target":   ParamKey,
	"target[]": ParamKey,
	// xFilesFactor changes how aggregation functions treat nulls; local
	// restricts a clustered graphite-web to its own store
	"xFilesFactor": ParamKey,
	"local":        ParamKey,
	"from":         ParamExtent,
	"until":        ParamExtent,
	"now":          ParamExtent,
	// tz only affects the parsing of date-anchored from/until values and the
	// rendering of timestamps in some formats
	"tz":            ParamExtent,
	"format":        ParamMarshal,
	"rawData":       ParamMarshal,
	"jsonp":         ParamMarshal,
	"pretty":        ParamMarshal,
	"noNullPoints":  ParamMarshal,
	"maxDataPoints": ParamMarshal,
	"cacheTimeout":  ParamUpstream,
	"noCache":       ParamUpstream,
	// maxStep constrains the fetch step at the origin, making the native
	// step unpredictable
	"maxStep": ParamDecline,
	// pickle is a non-series format
	"pickle": ParamDecline,
}

// UpstreamStripParams are never sent to the origin on the accelerated path:
// they would change the shape of the cached native-step data or its format
var UpstreamStripParams = []string{"maxDataPoints", "noNullPoints", "jsonp", "pretty", "format", "rawData"}

// SeriesFormats are the render output formats that carry time series data
// Trickster can model; everything else (png, svg, pdf, pickle, ...) is declined
var SeriesFormats = map[string]struct{}{"json": {}, "raw": {}, "csv": {}, "msgpack": {}}

// DefaultFormat is what graphite-web renders when no format is given
const DefaultFormat = "png"

// RenderParams is the parsed, classified view of a render request's
// parameters
type RenderParams struct {
	Targets       []string
	From          string
	Until         string
	Now           string
	TZ            string
	Format        string
	MaxDataPoints int
	NoNullPoints  bool
	JSONP         string
	Pretty        bool
	XFilesFactor  string
	Local         bool
	GraphType     string
	// Template holds template[name]=value parameters
	Template map[string]string
	// Declined is the frozen fallback reason when a parameter alone makes
	// the request non-accelerable, or empty
	Declined string
	// DeclinedParam names the parameter behind Declined
	DeclinedParam string
}

// ParseRenderParams classifies the parameters of a render request the way
// graphite-web's parseOptions reads them
func ParseRenderParams(v url.Values) RenderParams {
	p := RenderParams{GraphType: "line"}
	if t := v["target"]; len(t) > 0 {
		p.Targets = t
	} else if t := v["target[]"]; len(t) > 0 {
		p.Targets = t
	}
	p.From, p.Until, p.Now, p.TZ = v.Get("from"), v.Get("until"), v.Get("now"), v.Get("tz")
	if _, ok := v["pickle"]; ok {
		p.Format = "pickle"
	}
	if _, ok := v["rawData"]; ok {
		p.Format = "raw"
	}
	if f, ok := v["format"]; ok && len(f) > 0 {
		p.Format = f[0]
		p.JSONP = v.Get("jsonp")
	}
	if p.Format == "" {
		p.Format = DefaultFormat
	}
	p.Pretty = v.Get("pretty") != ""
	if m := v.Get("maxDataPoints"); isDigits(m) {
		p.MaxDataPoints, _ = strconv.Atoi(m)
	}
	_, p.NoNullPoints = v["noNullPoints"]
	p.XFilesFactor = v.Get("xFilesFactor")
	p.Local = v.Get("local") == "1"
	if g := v.Get("graphType"); g != "" {
		p.GraphType = g
	}
	for k, vals := range v {
		if strings.HasPrefix(k, "template[") && strings.HasSuffix(k, "]") && len(vals) > 0 {
			if p.Template == nil {
				p.Template = make(map[string]string)
			}
			p.Template[k[9:len(k)-1]] = vals[0]
		}
	}
	switch {
	case p.GraphType != "line":
		p.Declined, p.DeclinedParam = ReasonNonSeriesFormat, "graphType"
	case !isSeriesFormat(p.Format):
		p.Declined, p.DeclinedParam = ReasonNonSeriesFormat, "format"
	default:
		for k := range v {
			if RenderParamClasses[k] == ParamDecline && k != "pickle" {
				p.Declined, p.DeclinedParam = ReasonUnknownStep, k
				break
			}
		}
	}
	return p
}

func isSeriesFormat(f string) bool {
	_, ok := SeriesFormats[f]
	return ok
}
