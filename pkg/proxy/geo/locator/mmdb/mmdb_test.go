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

package mmdb

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	aclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/filesource"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/mmdb/options"
	"github.com/trickstercache/trickster/v2/pkg/testutil/geodb"

	"github.com/maxmind/mmdbwriter/mmdbtype"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	testInterval = timeconv.Duration(10 * time.Millisecond)
	waitTimeout  = 5 * time.Second
	waitTick     = 5 * time.Millisecond
	addrTexas    = "192.0.2.10"
	addrFrance   = "198.51.100.10"
	addrNoWhere  = "203.0.113.10"
	addrOnlyEU   = "100.64.0.10"
	addrV6Japan  = "2001:db8:1::10"
)

var (
	_ locator.Locator    = (*Locator)(nil)
	_ locator.BuildTimer = (*Locator)(nil)
)

func cityRecords() map[string]mmdbtype.Map {
	return map[string]mmdbtype.Map{
		"192.0.2.0/24":    geodb.GeoIP2("US", "NA", "TX"),
		"198.51.100.0/24": geodb.GeoIP2("FR", "EU", ""),
		"100.64.0.0/24":   geodb.GeoIP2("", "EU", ""),
		"2001:db8:1::/48": geodb.GeoIP2("JP", "AS", "13"),
		"203.0.113.0/24":  {"note": mmdbtype.String("no location")},
	}
}

func newLocator(t *testing.T, name string, db geodb.Options, edit func(*options.Options)) (*Locator, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.mmdb")
	geodb.Write(t, path, db)
	o := &options.Options{Path: path, ReloadInterval: testInterval}
	if edit != nil {
		edit(o)
	}
	l, err := New(name, o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func locate(t *testing.T, l *Locator, addr string) string {
	t.Helper()
	loc, err := l.Locate(netip.MustParseAddr(addr))
	require.NoError(t, err)
	return loc.String()
}

func TestGeoIP2City(t *testing.T) {
	built := time.Unix(1750000000, 0)
	l, _ := newLocator(t, "mmdb-city", geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Built: built,
		Records: cityRecords()}, nil)
	require.Equal(t, geo.FieldsAll, l.Serves())
	require.Equal(t, built, l.BuildTime())
	for addr, want := range map[string]string{
		addrTexas:              "US-TX",
		"::ffff:" + addrTexas:  "US-TX",
		addrFrance:             "FR",
		addrOnlyEU:             "continent:EU",
		addrNoWhere:            "",
		"10.0.0.1":             "",
		addrV6Japan:            "JP-13",
		"2001:db8:2::1":        "",
		"::ffff:" + addrFrance: "FR",
	} {
		require.Equal(t, want, locate(t, l, addr), addr)
		// a second lookup is answered from the record cache
		require.Equal(t, want, locate(t, l, addr), addr)
	}
	loc, err := l.Locate(netip.MustParseAddr(addrTexas))
	require.NoError(t, err)
	require.Equal(t, geo.Code2{'N', 'A'}, loc.Continent)
}

func TestSchemas(t *testing.T) {
	country := func(o *options.Options) { o.Schema = options.SchemaGeoIP2 }
	l, _ := newLocator(t, "mmdb-country", geodb.Options{DatabaseType: geodb.TypeGeoLite2Country,
		Records: map[string]mmdbtype.Map{"192.0.2.0/24": geodb.GeoIP2("US", "NA", "")}}, country)
	require.Equal(t, geo.FieldCountry|geo.FieldContinent, l.Serves(), "a Country file serves no subdivisions")

	l, _ = newLocator(t, "mmdb-ipinfo", geodb.Options{DatabaseType: geodb.TypeIPinfoLite,
		Records: map[string]mmdbtype.Map{"192.0.2.0/24": geodb.IPinfo("US", "NA")}}, nil)
	require.Equal(t, "US", locate(t, l, addrTexas))
	require.Equal(t, geo.FieldCountry|geo.FieldContinent, l.Serves())

	// an older IPinfo file keeps the codes under country and continent
	older := map[string]mmdbtype.Map{"192.0.2.0/24": {
		"country": mmdbtype.String("CA"), "continent": mmdbtype.String("NA"),
	}}
	l, _ = newLocator(t, "mmdb-ipinfo-old", geodb.Options{DatabaseType: "ipinfo country", Records: older}, nil)
	require.Equal(t, "CA", locate(t, l, addrTexas))

	custom := map[string]mmdbtype.Map{"192.0.2.0/24": {
		"geo": mmdbtype.Map{"cc": mmdbtype.String("MX"), "regions": mmdbtype.Slice{mmdbtype.String("MX-JAL")}},
	}}
	l, _ = newLocator(t, "mmdb-custom", geodb.Options{DatabaseType: geodb.TypeCustom, Records: custom},
		func(o *options.Options) {
			o.Schema = options.SchemaCustom
			o.Fields = &options.Fields{Country: []string{"geo", "cc"}, Subdivision: []string{"geo", "regions", "0"},
				Continent: []string{"geo", "continent"}}
		})
	require.Equal(t, "MX-JAL", locate(t, l, addrTexas))
	require.Equal(t, geo.FieldCountry|geo.FieldSubdivision, l.Serves())

	// a file auto cannot place fails to load, naming the custom schema
	path := filepath.Join(t.TempDir(), "custom.mmdb")
	geodb.Write(t, path, geodb.Options{DatabaseType: geodb.TypeCustom, Records: custom})
	_, err := New("mmdb-unplaced", &options.Options{Path: path, ReloadInterval: testInterval})
	require.ErrorIs(t, err, ErrNoSchema)
	_, err = New("mmdb-unplaced", &options.Options{Path: path, Schema: options.SchemaIPinfo,
		ReloadInterval: testInterval})
	require.ErrorIs(t, err, ErrNoSchema)
}

func TestIPv4Only(t *testing.T) {
	l, _ := newLocator(t, "mmdb-v4", geodb.Options{DatabaseType: geodb.TypeGeoLite2Country, IPVersion: 4,
		Records: map[string]mmdbtype.Map{"192.0.2.0/24": geodb.GeoIP2("US", "NA", "")}}, nil)
	require.Equal(t, "US", locate(t, l, addrTexas))
	require.Empty(t, locate(t, l, addrV6Japan), "an IPv6 client has no location in an IPv4 file")
}

func TestNotAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.mmdb")
	require.NoError(t, os.WriteFile(path, []byte("not a database"), 0o600))
	_, err := New("mmdb-junk", &options.Options{Path: path, ReloadInterval: testInterval})
	require.Error(t, err)
	_, err = New("mmdb-missing", &options.Options{Path: path + ".missing", ReloadInterval: testInterval})
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSwap(t *testing.T) {
	const name = "mmdb-swap"
	l, path := newLocator(t, name, geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Records: cityRecords()}, nil)
	refused := metrics.GeoLocatorReloads.WithLabelValues(name, filesource.ResultError)

	// lookups run throughout the swaps, which the race detector watches
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for !stop.Load() {
				_, _ = l.Locate(netip.MustParseAddr(addrTexas))
			}
		})
	}
	defer func() {
		stop.Store(true)
		wg.Wait()
	}()

	moved := cityRecords()
	moved["192.0.2.0/24"] = geodb.GeoIP2("CA", "NA", "QC")
	geodb.Write(t, path, geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Records: moved})
	require.Eventually(t, func() bool { return locate(t, l, addrTexas) == "CA-QC" }, waitTimeout, waitTick)

	// an in-place rewrite swaps too; a poll may read it part-written and refuse it first
	moved["192.0.2.0/24"] = geodb.GeoIP2("MX", "NA", "JAL")
	require.NoError(t, os.WriteFile(path, geodb.Bytes(t, geodb.Options{DatabaseType: geodb.TypeGeoLite2City,
		Records: moved}), 0o600))
	require.Eventually(t, func() bool { return locate(t, l, addrTexas) == "MX-JAL" }, waitTimeout, waitTick)

	a, err := acl.Compile(&aclopts.Options{Name: name, Deny: []string{"MX-JAL"}}, l, name)
	require.NoError(t, err)
	countryOnly := cityRecords()
	countryOnly["192.0.2.0/24"] = geodb.GeoIP2("MX", "NA", "")
	countryOnly["2001:db8:1::/48"] = geodb.GeoIP2("JP", "AS", "")
	for _, bad := range []struct {
		name string
		data []byte
	}{
		{"truncated", geodb.Bytes(t, geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Records: moved})[:100]},
		{"not a database", []byte("garbage")},
		// a Country file serves no subdivisions, which the file it replaces did
		{"fewer fields", geodb.Bytes(t, geodb.Options{DatabaseType: geodb.TypeGeoLite2Country,
			Records: map[string]mmdbtype.Map{"192.0.2.0/24": geodb.GeoIP2("BR", "SA", "")}})},
		// nor does a City file whose records name none
		{"city without subdivisions", geodb.Bytes(t, geodb.Options{DatabaseType: geodb.TypeGeoLite2City,
			Records: countryOnly})},
	} {
		before := testutil.ToFloat64(refused)
		tmp := path + ".tmp"
		require.NoError(t, os.WriteFile(tmp, bad.data, 0o600))
		require.NoError(t, os.Rename(tmp, path))
		require.Eventually(t, func() bool { return testutil.ToFloat64(refused) > before }, waitTimeout, waitTick,
			bad.name)
		require.Equal(t, "MX-JAL", locate(t, l, addrTexas), bad.name)
		require.Equal(t, acl.ResultDenied, a.Check(netip.MustParseAddr(addrTexas), acl.PlaneHTTP), bad.name)
	}
}

func TestSwapCustomSubdivisionLost(t *testing.T) {
	const name = "mmdb-custom-swap"
	custom := func(o *options.Options) {
		o.Schema = options.SchemaCustom
		o.Fields = &options.Fields{Country: []string{"geo", "cc"}, Subdivision: []string{"geo", "regions", "0"}}
	}
	l, path := newLocator(t, name, geodb.Options{DatabaseType: geodb.TypeCustom,
		Records: map[string]mmdbtype.Map{"192.0.2.0/24": {"geo": mmdbtype.Map{
			"cc": mmdbtype.String("US"), "regions": mmdbtype.Slice{mmdbtype.String("TX")},
		}}}}, custom)
	require.Equal(t, geo.FieldCountry|geo.FieldSubdivision, l.Serves())
	refused := metrics.GeoLocatorReloads.WithLabelValues(name, filesource.ResultError)
	before := testutil.ToFloat64(refused)

	// the replacement moves the region, so the configured path no longer reaches one
	geodb.Write(t, path, geodb.Options{DatabaseType: geodb.TypeCustom,
		Records: map[string]mmdbtype.Map{"192.0.2.0/24": {"geo": mmdbtype.Map{
			"cc": mmdbtype.String("US"), "region": mmdbtype.String("TX"),
		}}}})
	require.Eventually(t, func() bool { return testutil.ToFloat64(refused) > before }, waitTimeout, waitTick)
	require.Equal(t, "US-TX", locate(t, l, addrTexas))
}

func TestLateSubdivisions(t *testing.T) {
	// more networks than the schema sample, all placed by country alone, come before the first subdivision
	records := map[string]mmdbtype.Map{"192.0.2.0/24": geodb.GeoIP2("US", "NA", "TX")}
	for i := range sampleRecords + 10 {
		r := geodb.GeoIP2("CA", "NA", "")
		r["n"] = mmdbtype.Uint32(i)
		records[fmt.Sprintf("10.%d.%d.0/24", i/256, i%256)] = r
	}
	l, _ := newLocator(t, "mmdb-late", geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Records: records}, nil)
	require.Equal(t, geo.FieldsAll, l.Serves())
	require.Equal(t, "US-TX", locate(t, l, addrTexas))

	// with no subdivision anywhere, the file serves none
	delete(records, "192.0.2.0/24")
	l, _ = newLocator(t, "mmdb-late-none", geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Records: records},
		nil)
	require.Equal(t, geo.FieldCountry|geo.FieldContinent, l.Serves())
}

func TestMaxAge(t *testing.T) {
	old := geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Built: time.Now().Add(-90 * 24 * time.Hour),
		Records: cityRecords()}
	// an old file loads, and warns
	l, _ := newLocator(t, "mmdb-old", old, func(o *options.Options) {
		o.MaxAge = timeconv.Duration(30 * 24 * time.Hour)
	})
	require.Equal(t, "US-TX", locate(t, l, addrTexas))
}

func BenchmarkLocate(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bench.mmdb")
	geodb.Write(b, path, geodb.Options{DatabaseType: geodb.TypeGeoLite2City, Records: cityRecords()})
	l, err := New("mmdb-bench", &options.Options{Path: path, ReloadInterval: timeconv.Duration(time.Hour)})
	require.NoError(b, err)
	defer l.Close()
	addrs := []netip.Addr{netip.MustParseAddr(addrTexas), netip.MustParseAddr(addrFrance),
		netip.MustParseAddr(addrV6Japan), netip.MustParseAddr(addrNoWhere)}
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		_, _ = l.Locate(addrs[i%len(addrs)])
	}
}
