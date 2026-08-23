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
	"strings"
	"time"
)

// Fallback reasons: the frozen `reason` label values of
// trickster_graphite_fallbacks_total (implementation plan item 3.4). Every path
// that declines to accelerate a render request names exactly one of these.
const (
	ReasonParseError             = "parse_error"
	ReasonNonSeriesFormat        = "non_series_format"
	ReasonFunctionNotAllowlisted = "function_not_allowlisted"
	ReasonUnknownStep            = "unknown_step"
	ReasonMissingTarget          = "missing_target"
	ReasonMultiTargetMismatch    = "multi_target_step_mismatch"
	ReasonPassthroughMaxPoints   = "passthrough_max_data_points"
	ReasonMisprediction          = "misprediction"
)

// StepEffect describes what a function chain does to the step of the series
// it returns, relative to the native step of its leaves
type StepEffect int

const (
	// StepUnknown means the step cannot be predicted from the expression
	StepUnknown StepEffect = iota
	// StepInherit means the output step is the leaves' native step
	StepInherit
	// StepFixed means the output step is set by the expression (summarize)
	StepFixed
	// StepShift means the step is inherited but the fetch window is shifted
	// in time (timeShift); not decomposable in v1
	StepShift
)

func (s StepEffect) String() string {
	switch s {
	case StepInherit:
		return "inherit"
	case StepFixed:
		return "fixed"
	case StepShift:
		return "shift"
	}
	return "unknown"
}

// Classification is the two-property allowlist verdict for a target (D4).
// A target is eligible for the Delta Proxy Cache only when its step is
// predictable (Step != StepUnknown) AND every function in the chain is
// range-decomposable: the values for [t1,t2] are identical whether fetched
// alone or as part of a wider window.
type Classification struct {
	Step StepEffect
	// FixedStep is the output step when Step == StepFixed
	FixedStep time.Duration
	// Shift is the time offset when Step == StepShift
	Shift time.Duration
	// Decomposable is true when every function in the chain is
	// range-decomposable
	Decomposable bool
	// Leaves are the path expressions in series argument positions of the
	// target, in source order; scalar positions (summarize's interval and
	// function name) are excluded even when unquoted
	Leaves []string
	// Reason is the frozen fallback reason when the target is not
	// accelerable, or empty
	Reason string
	// Offender is the function (or construct) that caused the fallback
	Offender string
}

// Accelerable reports whether the target may take the DPC path
func (c Classification) Accelerable() bool {
	return c.Step != StepUnknown && c.Decomposable && len(c.Leaves) > 0
}

// funcSpec describes one allowlisted function
type funcSpec struct {
	step StepEffect
	// decomposable is the static answer; when conditional is set it is
	// evaluated against the call instead
	decomposable bool
	conditional  func(*Call) (StepEffect, time.Duration, bool)
	// seriesArgs is the number of leading positional arguments that are
	// series lists (0 means all of them). Only those positions contribute
	// leaf paths: an unquoted scalar such as the 1h in summarize(a.b, 1h)
	// parses as a path and must not be mistaken for a metric.
	seriesArgs int
}

var (
	// inherit: first positional argument is the series list
	inherit = funcSpec{step: StepInherit, decomposable: true, seriesArgs: 1}
	// inheritAll: every positional argument is a series list (*seriesLists)
	inheritAll = funcSpec{step: StepInherit, decomposable: true}
	// inherit2: the first two positional arguments are series lists
	inherit2 = funcSpec{step: StepInherit, decomposable: true, seriesArgs: 2}
)

// allowlist is the v1 two-property table. Anything absent is unknown and
// fails closed. Per D4 the list starts deliberately boring: cross-series
// aggregation, per-point arithmetic and naming/cosmetics, all of which are
// per-timestamp and so decompose over any range. Time-windowed functions
// (movingAverage,
// derivative, integral, holtWinters*, timeShift), whole-range selectors
// (highest*, lowest*, sortBy*, limit, *Percentile, *Above, *Below),
// generators (constantLine, timeFunction, ...), tag finders (seriesByTag) and
// template() are intentionally absent until Phase 12 evaluates them in
// Trickster over cached raw leaves.
var allowlist = map[string]funcSpec{
	// cross-series aggregation: one output point per input timestamp
	"sumSeries":                   inheritAll,
	"sum":                         inheritAll,
	"sumSeriesLists":              inherit2,
	"sumSeriesWithWildcards":      inherit,
	"averageSeries":               inheritAll,
	"avg":                         inheritAll,
	"averageSeriesWithWildcards":  inherit,
	"minSeries":                   inheritAll,
	"maxSeries":                   inheritAll,
	"diffSeries":                  inheritAll,
	"diffSeriesLists":             inherit2,
	"multiplySeries":              inheritAll,
	"multiplySeriesLists":         inherit2,
	"multiplySeriesWithWildcards": inherit,
	"stddevSeries":                inheritAll,
	"rangeOfSeries":               inheritAll,
	"countSeries":                 inheritAll,
	"percentileOfSeries":          inherit,
	"aggregate":                   inherit,
	"aggregateSeriesLists":        inherit2,
	"aggregateWithWildcards":      inherit,
	"group":                       inheritAll,
	"groupByNode":                 inherit,
	"groupByNodes":                inherit,
	"divideSeries":                inherit2,
	"divideSeriesLists":           inherit2,
	"asPercent":                   inherit2,
	"pct":                         inherit2,
	"weightedAverage":             inherit2,
	"powSeries":                   inheritAll,
	"unique":                      inheritAll,
	// per-point transforms
	"scale":            inherit,
	"scaleToSeconds":   inherit,
	"offset":           inherit,
	"add":              inherit,
	"pow":              inherit,
	"exp":              inherit,
	"absolute":         inherit,
	"invert":           inherit,
	"squareRoot":       inherit,
	"sigmoid":          inherit,
	"logit":            inherit,
	"log":              inherit,
	"round":            inherit,
	"transformNull":    inherit,
	"isNonNull":        inherit,
	"removeAboveValue": inherit,
	"removeBelowValue": inherit,
	"consolidateBy":    inherit,
	"setXFilesFactor":  inherit,
	"xFilesFactor":     inherit,
	// naming, filtering by name, and cosmetics
	"alias":          inherit,
	"aliasSub":       inherit,
	"aliasByNode":    inherit,
	"aliasByMetric":  inherit,
	"aliasByTags":    inherit,
	"upper":          inherit,
	"lower":          inherit,
	"substr":         inherit,
	"exclude":        inherit,
	"grep":           inherit,
	"color":          inherit,
	"alpha":          inherit,
	"lineWidth":      inherit,
	"dashed":         inherit,
	"drawAsInfinite": inherit,
	"secondYAxis":    inherit,
	"stacked":        inherit,
	"areaBetween":    inherit,
	// step-fixing, and none of them decomposable: a bucket is summarized
	// over the points that were fetched, so a bucket at the edge of the
	// requested window holds a partial value. Measured against graphite-web
	// 1.1.10: summarize(...,'1h') reported 134885.899, 302921.880 and
	// 307388.038 for the same absolute bucket over three different windows.
	// Caching such a value and reusing it for another window returns
	// incorrect data, so these take the unaccelerated lane until Trickster
	// evaluates them itself over cached native-resolution leaves (plan
	// item 12.1).
	"summarize":      {step: StepFixed, conditional: classifySummarize, seriesArgs: 1},
	"hitcount":       {step: StepFixed, conditional: classifyHitcount, seriesArgs: 1},
	"smartSummarize": {step: StepFixed, conditional: classifySmartSummarize, seriesArgs: 1},
	// time-shifting: step inherits, fetch window moves; not decomposable in v1
	"timeShift": {step: StepShift, conditional: classifyTimeShift, seriesArgs: 1},
}

// seriesLeaves collects the path expressions that are series arguments of
// allowlisted calls, ignoring scalar positions. It stops at the first call
// that is not allowlisted; Classify reports that separately.
func seriesLeaves(n Node, out []string) []string {
	switch t := n.(type) {
	case *Path:
		return append(out, t.Expr)
	case *Call:
		spec, ok := allowlist[t.Func]
		if !ok {
			// not accelerable anyway; most graphite functions take the
			// series list first, which is enough to name the leaves
			if len(t.Args) > 0 {
				out = seriesLeaves(t.Args[0], out)
			}
			return out
		}
		for i, a := range t.Args {
			if spec.seriesArgs > 0 && i >= spec.seriesArgs {
				break
			}
			out = seriesLeaves(a, out)
		}
	case *Template:
		return seriesLeaves(t.Inner, out)
	}
	return out
}

// Classify evaluates the allowlist over a parsed target
func Classify(n Node) Classification {
	c := Classification{Step: StepInherit, Decomposable: true, Leaves: seriesLeaves(n, nil)}
	var fixed, shift time.Duration
	fixedCount := 0
	Walk(n, func(n Node) bool {
		switch t := n.(type) {
		case *Template:
			c.fail(ReasonFunctionNotAllowlisted, "template")
			return false
		case *Call:
			spec, ok := allowlist[t.Func]
			if !ok {
				c.fail(ReasonFunctionNotAllowlisted, t.Func)
				return false
			}
			step, d, decomposable := spec.step, time.Duration(0), spec.decomposable
			if spec.conditional != nil {
				step, d, decomposable = spec.conditional(t)
			}
			if step == StepUnknown {
				c.fail(ReasonParseError, t.Func)
				return false
			}
			if !decomposable {
				c.fail(ReasonFunctionNotAllowlisted, t.Func)
				return false
			}
			switch step {
			case StepFixed:
				// a step-fixing call anywhere in the tree sets the output
				// step (graphite normalizes mixed steps to their LCM, so two
				// distinct fixed steps are not predictable: fail closed)
				if fixedCount > 0 && d != fixed {
					c.fail(ReasonUnknownStep, t.Func)
					return false
				}
				fixed, fixedCount = d, fixedCount+1
			case StepShift:
				shift = d
			}
		}
		return true
	})
	if c.Step == StepUnknown {
		return c
	}
	if len(c.Leaves) == 0 {
		// a target with no metric path is a generator (constantLine,
		// timeFunction, ...) or a tag expression: nothing to resolve
		c.fail(ReasonFunctionNotAllowlisted, describe(n))
		return c
	}
	switch {
	case fixedCount > 0:
		c.Step, c.FixedStep = StepFixed, fixed
	case shift != 0:
		c.Step, c.Shift = StepShift, shift
	}
	return c
}

func (c *Classification) fail(reason, offender string) {
	c.Step, c.Decomposable = StepUnknown, false
	c.Reason, c.Offender = reason, offender
}

func describe(n Node) string {
	if c, ok := n.(*Call); ok {
		return c.Func
	}
	return "expression"
}

// argValue returns the i'th positional argument, or the keyword argument
// named name, of a call
func argValue(c *Call, i int, name string) Node {
	if i < len(c.Args) {
		return c.Args[i]
	}
	for _, k := range c.KwArgs {
		if k.Name == name {
			return k.Value
		}
	}
	return nil
}

// intervalArg reads a graphite interval string argument (unquoted 1h or
// quoted "1h"), as summarize and friends accept it
func intervalArg(n Node) (time.Duration, bool) {
	var s string
	switch t := n.(type) {
	case *Path:
		s = t.Expr
	case *String:
		s = t.Value
	default:
		return 0, false
	}
	d, err := ParseTimeOffset(s)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// boolArg reads a boolean argument; graphite also accepts the unquoted
// words true/false, which the grammar yields as Bool nodes
func boolArg(n Node, def bool) (bool, bool) {
	switch t := n.(type) {
	case nil:
		return def, true
	case *Bool:
		return t.Value, true
	case *Number:
		return t.Text != "0", true
	case *String:
		return strings.EqualFold(t.Value, "true"), true
	}
	return false, false
}

// summarize(series, interval, func='sum', alignToFrom=False) fixes the
// output step to the interval, and is never decomposable: the bucket
// covering an edge of the requested window summarizes only the points
// inside that window, so its value changes with the window. The arguments
// are still parsed, so a malformed interval is a parse error rather than an
// unrecognized function.
func classifySummarize(c *Call) (StepEffect, time.Duration, bool) {
	d, ok := intervalArg(argValue(c, 1, "intervalString"))
	if !ok {
		return StepUnknown, 0, false
	}
	if _, ok := boolArg(argValue(c, 3, "alignToFrom"), false); !ok {
		return StepUnknown, 0, false
	}
	return StepFixed, d, false
}

// hitcount(series, interval, alignToInterval=False) buckets like summarize
func classifyHitcount(c *Call) (StepEffect, time.Duration, bool) {
	d, ok := intervalArg(argValue(c, 1, "intervalString"))
	if !ok {
		return StepUnknown, 0, false
	}
	if _, ok := boolArg(argValue(c, 2, "alignToInterval"), false); !ok {
		return StepUnknown, 0, false
	}
	return StepFixed, d, false
}

// smartSummarize aligns relative to the window: step is known, never
// decomposable
func classifySmartSummarize(c *Call) (StepEffect, time.Duration, bool) {
	d, ok := intervalArg(argValue(c, 1, "intervalString"))
	if !ok {
		return StepUnknown, 0, false
	}
	return StepFixed, d, false
}

// timeShift(series, shift, resetEnd=True, alignDST=False): shift is parsed
// by parseTimeOffset and defaults to negative when unsigned
func classifyTimeShift(c *Call) (StepEffect, time.Duration, bool) {
	n := argValue(c, 1, "timeShift")
	var s string
	switch t := n.(type) {
	case *Path:
		s = t.Expr
	case *String:
		s = t.Value
	default:
		return StepUnknown, 0, false
	}
	if s != "" && s[0] >= '0' && s[0] <= '9' {
		s = "-" + s
	}
	d, err := ParseTimeOffset(s)
	if err != nil {
		return StepUnknown, 0, false
	}
	return StepShift, d, false
}
