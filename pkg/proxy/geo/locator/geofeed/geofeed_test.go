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

package geofeed

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/filesource"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/feed"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	testLocatorName = "test-geofeed"
	testInterval    = timeconv.Duration(10 * time.Millisecond)
	waitTimeout     = 5 * time.Second
	waitTick        = 5 * time.Millisecond
)

var _ locator.Locator = (*Locator)(nil)

func country(t *testing.T, l *Locator, addr string) string {
	t.Helper()
	loc, err := l.Locate(netip.MustParseAddr(addr))
	require.NoError(t, err)
	return loc.String()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(content), 0o600))
	require.NoError(t, os.Rename(tmp, path))
}

func TestInline(t *testing.T) {
	l, err := New(testLocatorName, &options.Options{Entries: []string{
		"203.0.113.0/24,DE,DE-BE",
		"203.0.113.128/25,FR",
		"203.0.113.200,,,,",
		"0.0.0.0/0,US",
		"2001:db8::/32,JP",
		"2001:db8:cafe::/48,AU",
		"2001:db8:cafe::1,NZ",
		"# a comment",
	}})
	require.NoError(t, err)
	defer l.Close()
	require.Equal(t, geo.FieldsAll, l.Serves())
	require.Equal(t, 7, l.Len())
	for addr, want := range map[string]string{
		"203.0.113.5":          "DE-BE",
		"203.0.113.130":        "FR",
		"203.0.113.200":        "", // a line with no location places no one
		"198.51.100.1":         "US",
		"::ffff:203.0.113.130": "FR",
		"2001:db8::1":          "JP",
		"2001:db8:cafe::2":     "AU",
		"2001:db8:cafe::1":     "NZ",
		"2001:db9::1":          "",
	} {
		require.Equal(t, want, country(t, l, addr), addr)
	}
	loc, err := l.Locate(netip.Addr{})
	require.NoError(t, err)
	require.True(t, loc.IsZero())
	loc, err = l.Locate(netip.MustParseAddr("203.0.113.5"))
	require.NoError(t, err)
	require.Equal(t, geo.Code2{'E', 'U'}, loc.Continent)

	_, err = New(testLocatorName, &options.Options{Entries: []string{"bad,US"}})
	require.ErrorIs(t, err, feed.ErrInvalidLine)
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.csv"), filepath.Join(dir, "b.csv")
	writeFile(t, a, "192.0.2.0/24,US\n198.51.100.0/24,CA\n")
	writeFile(t, b, "2001:db8::/32,MX\nbad line,US\n")
	l, err := New(testLocatorName, &options.Options{
		Entries:        []string{"198.51.100.0/24,GB"},
		Files:          []string{a, b},
		ReloadInterval: testInterval,
	})
	require.NoError(t, err)
	defer l.Close()
	require.Equal(t, "US", country(t, l, "192.0.2.1"))
	require.Equal(t, "GB", country(t, l, "198.51.100.1"), "an inline entry beats a file's")
	require.Equal(t, "MX", country(t, l, "2001:db8::1"))

	writeFile(t, a, "192.0.2.0/24,BR\n")
	require.Eventually(t, func() bool { return country(t, l, "192.0.2.1") == "BR" }, waitTimeout, waitTick)

	writeFile(t, b, "# nothing here\n")
	require.Eventually(t, func() bool { return country(t, l, "2001:db8::1") == "" }, waitTimeout, waitTick)

	// files that lose every entry are refused, and the last good ones serve on
	refused := metrics.GeoLocatorReloads.WithLabelValues(testLocatorName, filesource.ResultError)
	before := testutil.ToFloat64(refused)
	writeFile(t, a, "")
	require.Eventually(t, func() bool { return testutil.ToFloat64(refused) > before }, waitTimeout, waitTick)
	require.Equal(t, "BR", country(t, l, "192.0.2.1"))

	writeFile(t, a, "192.0.2.0/24,AR\n")
	require.Eventually(t, func() bool { return country(t, l, "192.0.2.1") == "AR" }, waitTimeout, waitTick)
}

func TestFilesFirstLoadFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.csv")
	_, err := New(testLocatorName, &options.Options{Files: []string{missing}, ReloadInterval: testInterval})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func BenchmarkLocate(b *testing.B) {
	for _, n := range []int{10, 300000} {
		b.Run(fmt.Sprintf("entries=%d", n), func(b *testing.B) {
			entries := make([]string, 0, n)
			for i := range n {
				if i%2 == 0 {
					entries = append(entries, fmt.Sprintf("10.%d.%d.0/24,US", i>>16&255, i>>8&255))
				} else {
					entries = append(entries, fmt.Sprintf("2001:db8:%x:%x::/64,CA", i>>16, i&0xffff))
				}
			}
			l, err := New(testLocatorName, &options.Options{Entries: entries})
			require.NoError(b, err)
			addrs := []netip.Addr{
				netip.MustParseAddr("10.0.1.7"), netip.MustParseAddr("2001:db8:0:1::7"),
				netip.MustParseAddr("192.0.2.1"),
			}
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				_, _ = l.Locate(addrs[i%len(addrs)])
			}
		})
	}
}
