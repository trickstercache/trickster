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
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelch "github.com/trickstercache/trickster/v2/pkg/backends/clickhouse/model"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

// zoneServer answers the zone probe with name, or fails it with status
func zoneServer(t *testing.T, name string, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var probes atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get(upQuery) == zoneQuery {
			probes.Add(1)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, name+"\n")
	}))
	t.Cleanup(ts.Close)
	return ts, &probes
}

func TestServerZoneProbe(t *testing.T) {
	ts, probes := zoneServer(t, "America/New_York", http.StatusOK)
	base, _ := url.Parse(ts.URL)
	var z serverZone
	_, ok := z.get()
	require.False(t, ok)
	require.True(t, z.probe(context.Background(), ts.Client(), base))
	loc, ok := z.get()
	require.True(t, ok)
	require.Equal(t, "America/New_York", loc.String())
	// a known zone isn't probed again
	require.True(t, z.resolve(context.Background(), ts.Client(), base))
	require.Equal(t, int32(1), probes.Load())
	// a later response's zone replaces it, and UTC is nil
	z.set("UTC")
	loc, ok = z.get()
	require.True(t, ok)
	require.Nil(t, loc)
	z.set("Not/AZone")
	_, ok = z.get()
	require.True(t, ok)
}

func TestServerZoneProbeFails(t *testing.T) {
	for name, ts := range map[string]*httptest.Server{
		"status":  func() *httptest.Server { s, _ := zoneServer(t, "UTC", http.StatusUnauthorized); return s }(),
		"no zone": func() *httptest.Server { s, _ := zoneServer(t, "{}", http.StatusOK); return s }(),
	} {
		t.Run(name, func(t *testing.T) {
			base, _ := url.Parse(ts.URL)
			var z serverZone
			require.False(t, z.probe(context.Background(), ts.Client(), base))
			// a failed probe isn't retried at once; one request is proxied to learn the zone, and the
			// next takes it as UTC
			require.False(t, z.resolve(context.Background(), ts.Client(), base))
			require.True(t, z.resolve(context.Background(), ts.Client(), base))
			loc, ok := z.get()
			require.True(t, ok)
			require.Nil(t, loc)
		})
	}
	var z serverZone
	base, _ := url.Parse("http://127.0.0.1:1")
	require.False(t, z.probe(context.Background(), &http.Client{Timeout: time.Second}, base))
	base.Host = "\x00"
	z.retryAt = time.Time{}
	require.False(t, z.probe(context.Background(), http.DefaultClient, base))
}

func TestZoneObserver(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(modelch.TimezoneHeader, r.URL.Query().Get("zone"))
	}))
	defer ts.Close()
	var z serverZone
	client := &http.Client{Transport: &zoneObserver{next: http.DefaultTransport, zone: &z}}
	get := func(rawQuery string) {
		resp, err := client.Get(ts.URL + "/?" + rawQuery)
		require.NoError(t, err)
		resp.Body.Close()
	}
	// a request that sets its session's zone doesn't name the server's
	get("zone=Asia/Tokyo&session_timezone=Asia/Tokyo")
	get("zone=Asia/Tokyo&query=" + url.QueryEscape("SELECT 1 SETTINGS session_timezone='Asia/Tokyo'"))
	_, ok := z.get()
	require.False(t, ok)
	get("zone=Europe/Dublin")
	loc, ok := z.get()
	require.True(t, ok)
	require.Equal(t, "Europe/Dublin", loc.String())
	// a body is read for the setting only once it's been read
	post := func(body string, cached bool) bool {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		if cached {
			r = request.SetResources(r, &request.Resources{RequestBody: []byte(body)})
		}
		return setsSessionZone(r)
	}
	require.True(t, post("SELECT 1 SETTINGS session_timezone='UTC'", true))
	require.False(t, post("SELECT 1", true))
	require.True(t, post("SELECT 1", false))
}

func TestSessionZone(t *testing.T) {
	loc, ok := sessionZone(url.Values{modelch.SettingSessionTimezone: {"Asia/Tokyo"}}, "")
	require.True(t, ok)
	require.Equal(t, "Asia/Tokyo", loc.String())
	loc, ok = sessionZone(url.Values{}, "SELECT 1 SETTINGS max_threads=1, Session_Timezone = 'Europe/Dublin'")
	require.True(t, ok)
	require.Equal(t, "Europe/Dublin", loc.String())
	_, ok = sessionZone(url.Values{}, "SELECT 1")
	require.False(t, ok)
	_, ok = sessionZone(url.Values{modelch.SettingSessionTimezone: {"Not/AZone"}}, "")
	require.False(t, ok)
}

func TestParseTimeRangeQueryFormatOptions(t *testing.T) {
	client := &Client{}
	client.zone.set("Europe/Dublin")
	parse := func(rawQuery string) modelch.FormatOptions {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://blah.com/?"+rawQuery, nil)
		_, ro, _, err := client.ParseTimeRangeQuery(r)
		require.NoError(t, err)
		return ro.ProviderRequest.(modelch.FormatOptions)
	}
	// the server's zone, unless the request sets its session's
	fopts := parse(testRawQuery())
	require.Equal(t, "Europe/Dublin", fopts.ZoneName())
	fopts = parse(testRawQuery() + "&session_timezone=Asia/Tokyo&date_time_output_format=iso" +
		"&output_format_json_quote_64bit_integers=1")
	require.Equal(t, modelch.FormatOptions{Zone: fopts.Zone, DateTimeFormat: modelch.DateTimeISO, QuoteInt64: true}, fopts)
	require.Equal(t, "Asia/Tokyo", fopts.ZoneName())
}

func TestQueryHandlerLearnsTheZone(t *testing.T) {
	// an origin that names no zone and fails the probe: the first query is proxied, and the next cached
	ts, probes := zoneServer(t, "{}", http.StatusOK)
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	require.NoError(t, err)
	client := backendClient.(*Client)
	base, _ := url.Parse(ts.URL)
	require.False(t, client.zone.resolve(context.Background(), ts.Client(), base))
	require.Equal(t, int32(1), probes.Load())
	require.True(t, client.zone.resolve(context.Background(), ts.Client(), base))
	loc, ok := client.zone.get()
	require.True(t, ok)
	require.Nil(t, loc)
}

func TestCacheKeyIgnoresRendering(t *testing.T) {
	client := &Client{}
	client.zone.set("UTC")
	const bounds = " FROM e WHERE ts >= toDateTime(1790553600) AND ts < toDateTime(1790726400) GROUP BY t ORDER BY t"
	elements := func(query, params string) (map[string]string, error) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://blah.com/?"+url.Values{upQuery: {query}}.Encode()+params, nil)
		trq, _, canOPC, err := client.ParseTimeRangeQuery(r)
		if err != nil {
			require.True(t, canOPC)
			return nil, err
		}
		return trq.CacheKeyElements, nil
	}
	hourly := "SELECT toStartOfHour(ts) AS t, count() AS c" + bounds
	base, err := elements(hourly, "")
	require.NoError(t, err)
	// the same rows written another way, or in a zone whose offsets fall on the hours, share a key
	for _, params := range []string{
		"&date_time_output_format=iso&output_format_json_quote_64bit_integers=1&default_format=JSON",
		"&session_timezone=America/New_York", "&session_timezone=UTC", "&client_protocol_version=54460",
	} {
		got, err := elements(hourly, params)
		require.NoError(t, err)
		require.Equal(t, base, got, params)
	}
	// rows that differ by zone don't: hours a zone starts on the half hour, and days
	for query, params := range map[string]string{
		hourly: "&session_timezone=Asia/Kolkata",
		"SELECT toStartOfDay(ts) AS t, count() AS c" + bounds: "&session_timezone=America/New_York",
		"SELECT toStartOfHour(ts) AS t, count() AS c FROM e WHERE ts >= toDateTime(1790553600) AND " +
			"ts < toDateTime(1790726400) AND toHour(ts) > 8 GROUP BY t ORDER BY t": "&session_timezone=America/New_York",
	} {
		zoned, err := elements(query, params)
		require.NoError(t, err)
		utc, err := elements(query, "")
		require.NoError(t, err)
		require.NotEqual(t, utc[modelch.SettingSessionTimezone], zoned[modelch.SettingSessionTimezone], query)
	}
	// the server's zone counts as the session's
	client.zone.set("America/New_York")
	daily, err := elements("SELECT toStartOfDay(ts) AS t, count() AS c"+bounds, "")
	require.NoError(t, err)
	require.Equal(t, "America/New_York", daily[modelch.SettingSessionTimezone])
	// text bounds are only read as the analysis reads them in UTC
	text := "SELECT toStartOfHour(ts) AS t, count() AS c FROM e WHERE ts >= '2026-09-29 00:00:00' AND ts < '2026-09-29 06:00:00' GROUP BY t"
	_, err = elements(text, "")
	require.ErrorIs(t, err, ErrZonedBounds)
	_, err = elements(text, "&session_timezone=UTC")
	require.NoError(t, err)
}

func TestOffsetsAlign(t *testing.T) {
	lh, _ := time.LoadLocation("Australia/Lord_Howe")
	ny, _ := time.LoadLocation("America/New_York")
	// Lord Howe's clock moves half an hour, so a range across its change aligns to half hours only
	across := timeseries.Extent{Start: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)}
	require.False(t, offsetsAlign(lh, time.Hour, across))
	require.True(t, offsetsAlign(lh, 30*time.Minute, across))
	require.True(t, offsetsAlign(ny, time.Hour, across))
	require.False(t, offsetsAlign(ny, 24*time.Hour, across))
	require.False(t, offsetsAlign(ny, 0, across))
}
