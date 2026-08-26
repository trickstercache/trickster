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

package resolution

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"strconv"
	"strings"
	"time"

	tspan "github.com/trickstercache/trickster/v2/pkg/observability/tracing/span"

	"go.opentelemetry.io/otel/attribute"
)

// ProbeResult is the outcome of one probe
type ProbeResult struct {
	Kind string
	// Result is ResultStep, ResultEmpty or ResultError
	Result string
	// Step, Start and End come from the raw header when Result == ResultStep
	Step       time.Duration
	Start, End time.Time
	Err        error
}

// Prober issues format=raw probes (step in the header), never maxDataPoints
// except Learner.maxRetention's one wide probe. Every probe pins graphite-web's
// reference time with `now` so that `now - from` is exactly the requested age.
type Prober struct {
	Origin   *Origin
	Observer Observer
	Tracers  *Tracers
}

// narrowWindow is the narrow probe's until - from: whisper returns at least
// one point inside retention and nothing beyond it, so 1s answers both.
const narrowWindow = time.Second

// Narrow probes from=now-age, until=from+1s: the step of the rung serving
// that age inside retention, or empty beyond maxRetention.
func (p *Prober) Narrow(ctx context.Context, leaf string, age time.Duration, now time.Time) ProbeResult {
	from := now.Add(-age)
	return p.run(ctx, KindNarrow, leaf, from, from.Add(narrowWindow), now, nil)
}

// Wide probes from=now-age, until=now: the step a real query of that age
// receives, and the clamped start past maxRetention. extra is for maxRetention only.
func (p *Prober) Wide(ctx context.Context, leaf string, age time.Duration, now time.Time,
	extra url.Values,
) ProbeResult {
	return p.run(ctx, KindWide, leaf, now.Add(-age), now, now, extra)
}

func (p *Prober) run(ctx context.Context, kind, leaf string, from, until, now time.Time,
	extra url.Values,
) ProbeResult {
	q := url.Values{
		"target": {leaf},
		"from":   {strconv.FormatInt(from.Unix(), 10)},
		"until":  {strconv.FormatInt(until.Unix(), 10)},
		"now":    {strconv.FormatInt(now.Unix(), 10)},
		"format": {"raw"},
	}
	maps.Copy(q, extra)
	ctx, span := tspan.NewChildSpan(ctx, p.Tracers.Get(), "GraphiteProbe")
	if span != nil {
		defer span.End()
	}
	res := ProbeResult{Kind: kind}
	body, err := p.Origin.Get(ctx, "/render", q)
	if err != nil {
		res.Result, res.Err = ResultError, err
		p.observe(kind, ResultError)
		return res
	}
	r, ok, err := parseRawHeader(body)
	switch {
	case err != nil:
		res.Result, res.Err = ResultError, err
	case !ok:
		res.Result = ResultEmpty
	default:
		res.Result, res.Step, res.Start, res.End = ResultStep, r.step, r.start, r.end
	}
	p.observe(kind, res.Result)
	tspan.SetAttributes(p.Tracers.Get(), span,
		attribute.String("graphite.probe.kind", kind),
		attribute.String("graphite.probe.result", res.Result),
		attribute.String("graphite.probe.step", res.Step.String()),
	)
	return res
}

func (p *Prober) observe(kind, result string) {
	if p.Observer != nil {
		p.Observer.Probe(kind, result)
	}
}

type rawHeader struct {
	target     string
	start, end time.Time
	step       time.Duration
}

var errBadRaw = errors.New("malformed raw render response")

// reads the first series header of a format=raw body:
// <target>,<start>,<end>,<step>|<values>. ok is false for an empty body.
func parseRawHeader(body []byte) (rawHeader, bool, error) {
	s := strings.TrimSpace(string(body))
	if s == "" {
		return rawHeader{}, false, nil
	}
	line, _, _ := strings.Cut(s, "\n")
	head, _, found := strings.Cut(line, "|")
	if !found {
		return rawHeader{}, false, errBadRaw
	}
	i := strings.LastIndexByte(head, ',')
	if i < 0 {
		return rawHeader{}, false, errBadRaw
	}
	step, err := strconv.ParseInt(head[i+1:], 10, 64)
	head = head[:i]
	if err != nil || step <= 0 {
		return rawHeader{}, false, errBadRaw
	}
	i = strings.LastIndexByte(head, ',')
	if i < 0 {
		return rawHeader{}, false, errBadRaw
	}
	end, err := strconv.ParseInt(head[i+1:], 10, 64)
	head = head[:i]
	if err != nil {
		return rawHeader{}, false, errBadRaw
	}
	i = strings.LastIndexByte(head, ',')
	if i < 0 {
		return rawHeader{}, false, errBadRaw
	}
	start, err := strconv.ParseInt(head[i+1:], 10, 64)
	if err != nil {
		return rawHeader{}, false, errBadRaw
	}
	return rawHeader{
		target: head[:i], start: time.Unix(start, 0), end: time.Unix(end, 0),
		step: time.Duration(step) * time.Second,
	}, true, nil
}
