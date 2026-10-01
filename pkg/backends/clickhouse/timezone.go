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

package clickhouse

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	modelch "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/model"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/methods"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	// the query that asks the server for its time zone, and how long the asking may take
	zoneQuery        = "SELECT timezone() FORMAT TSV"
	zoneProbeTimeout = 5 * time.Second
	// how long after a failed probe the next may be made
	zoneProbeRetry = 10 * time.Second
	// the most of a probe's response that's read
	zoneProbeLimit = 256
)

// errZoneProbe indicates the server didn't answer the zone probe
var errZoneProbe = errors.New("ClickHouse time zone probe failed")

// sessionZoneSetting finds a SETTINGS clause's session_timezone in a statement
var sessionZoneSetting = regexp.MustCompile(`(?i)\bsession_timezone\s*=\s*'([^']*)'`)

// serverZone is the time zone the server writes a DateTime in when a request sets none. It's probed
// when the backend starts, and learned from each response that names it.
type serverZone struct {
	// the zone, nil for UTC; nil until known
	known   atomic.Pointer[zoneState]
	mu      sync.Mutex
	retryAt time.Time
	// whether a request was proxied to learn the zone, after which it's taken as UTC until a response names it
	proxied bool
}

type zoneState struct {
	loc *time.Location
}

// get returns the server's zone, nil for UTC, and whether it's known
func (z *serverZone) get() (*time.Location, bool) {
	if s := z.known.Load(); s != nil {
		return s.loc, true
	}
	return nil, false
}

// set records the server's zone by name, unless the name isn't a zone
func (z *serverZone) set(name string) {
	loc, ok := modelch.LoadZone(name)
	if !ok {
		return
	}
	if s := z.known.Load(); s == nil || s.loc != loc {
		z.known.Store(&zoneState{loc: loc})
	}
}

// probe asks the server for its zone, unless it's known or a probe failed too recently, and reports
// whether the zone is known after
func (z *serverZone) probe(ctx context.Context, client *http.Client, base *url.URL) bool {
	if _, ok := z.get(); ok {
		return true
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if _, ok := z.get(); ok {
		return true
	}
	if time.Now().Before(z.retryAt) {
		return false
	}
	name, err := askZone(ctx, client, base)
	if err != nil {
		z.retryAt = time.Now().Add(zoneProbeRetry)
		logger.Debug("could not probe the ClickHouse time zone", logging.Pairs{"error": err})
		return false
	}
	z.set(name)
	_, ok := z.get()
	return ok
}

// resolve reports whether a request may be cached: the zone is known or probed, or one request was
// proxied already to learn it, so it's taken as UTC; otherwise this one is proxied to learn it.
func (z *serverZone) resolve(ctx context.Context, client *http.Client, base *url.URL) bool {
	if z.probe(ctx, client, base) {
		return true
	}
	z.mu.Lock()
	defer z.mu.Unlock()
	if _, ok := z.get(); ok {
		return true
	}
	if !z.proxied {
		z.proxied = true
		return false
	}
	logger.Warn("ClickHouse time zone is unknown, taking it as UTC", nil)
	z.known.Store(&zoneState{})
	return true
}

// askZone returns the zone the server names for itself
func askZone(ctx context.Context, client *http.Client, base *url.URL) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, zoneProbeTimeout)
	defer cancel()
	u := *base
	u.RawQuery = url.Values{upQuery: {zoneQuery}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, zoneProbeLimit))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", errZoneProbe
	}
	return strings.TrimSpace(string(body)), nil
}

// zoneObserver learns the server's zone from the responses that name it, those to requests that don't
// set their own
type zoneObserver struct {
	next http.RoundTripper
	zone *serverZone
}

func (o *zoneObserver) RoundTrip(r *http.Request) (*http.Response, error) {
	sets := setsSessionZone(r)
	resp, err := o.next.RoundTrip(r)
	if err == nil && resp != nil && !sets {
		if name := resp.Header.Get(modelch.TimezoneHeader); name != "" {
			o.zone.set(name)
		}
	}
	return resp, err
}

// setsSessionZone reports whether a request may set its session's zone: in its URL, its query, or its
// body, which is only read when it's been read already
func setsSessionZone(r *http.Request) bool {
	if r.URL != nil && strings.Contains(r.URL.RawQuery, modelch.SettingSessionTimezone) {
		return true
	}
	if !methods.HasBody(r.Method) {
		return false
	}
	rsc := request.GetResources(r)
	return rsc == nil || rsc.RequestBody == nil ||
		bytes.Contains(rsc.RequestBody, []byte(modelch.SettingSessionTimezone))
}

// sessionZone returns the zone a request sets for its session, in its URL or its statement's SETTINGS,
// nil for UTC, and whether it sets one
func sessionZone(settings url.Values, statement string) (*time.Location, bool) {
	name := settings.Get(modelch.SettingSessionTimezone)
	if name == "" {
		m := sessionZoneSetting.FindStringSubmatch(statement)
		if m == nil {
			return nil, false
		}
		name = m[1]
	}
	return modelch.LoadZone(name)
}

// renderSettings change only how a response is written, which a cached query writes for each request
var renderSettings = []string{
	modelch.SettingDateTimeOutput, modelch.SettingQuoteInt64, modelch.SettingQuoteDecimals,
	modelch.SettingQuoteDenormals, "default_format", "client_protocol_version",
}

// keyRendering keys a delta-cached query on what changes its rows, not on how they're written: no
// rendering setting, and the session's zone only when its rows depend on it
func keyRendering(trq *timeseries.TimeRangeQuery, plan *sqlanalyzer.QueryPlan, zone *time.Location) {
	if trq.CacheKeyElements == nil {
		trq.CacheKeyElements = make(map[string]string, len(renderSettings)+1)
	}
	for _, name := range renderSettings {
		trq.CacheKeyElements[name] = ""
	}
	trq.CacheKeyElements[modelch.SettingSessionTimezone] = zoneKey(plan, zone, trq.Extent)
}

// zoneKey returns the session zone's part of a query's cache key: the zone, unless only the bucket
// reads it and its offsets fall on bucket boundaries, so the rows are the same in every zone
func zoneKey(plan *sqlanalyzer.QueryPlan, zone *time.Location, extent timeseries.Extent) string {
	if zone == nil || (!plan.ReadsZone && offsetsAlign(zone, plan.Step, extent)) {
		return ""
	}
	return zone.String()
}

// offsetsAlign reports whether each offset from UTC the zone has over the extent is a whole number of
// steps, so a bucket the zone's clock starts starts at the same instant in UTC
func offsetsAlign(zone *time.Location, step time.Duration, extent timeseries.Extent) bool {
	if step <= 0 {
		return false
	}
	for at := extent.Start; ; {
		local := at.In(zone)
		_, offset := local.Zone()
		if time.Duration(offset)*time.Second%step != 0 {
			return false
		}
		_, end := local.ZoneBounds()
		if end.IsZero() || !end.Before(extent.End) {
			return true
		}
		at = end
	}
}
