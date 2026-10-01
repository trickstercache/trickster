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

package options

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config/reserved"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	headeropts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header/options"
	mmdbopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/mmdb/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const testName = "corrections"

func TestInitialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.mmdb")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	l := Lookup{}
	require.NoError(t, yaml.Unmarshal([]byte(`
db:
  provider: " MMDB "
  mmdb:
    path: `+path+`
edge:
  provider: header
feed:
  provider: geofeed
  geofeed:
    entries: ["192.0.2.0/24,US"]
skipped:
`), &l))
	l.Initialize()
	require.Equal(t, providers.MMDB, l["db"].Provider)
	require.Equal(t, "db", l["db"].Name)
	require.NotZero(t, l["db"].MMDB.ReloadInterval)
	require.Equal(t, headeropts.DefaultUnknownValues, l["edge"].Header.UnknownValues)
	require.NotZero(t, l["feed"].Geofeed.ReloadInterval)
	require.ErrorIs(t, l.Validate(), headeropts.ErrNoCountryHeader)
	l["edge"].Header.Country = "CF-IPCountry"
	require.NoError(t, l.Validate())

	c := l.Clone()
	require.Equal(t, l, c)
	require.True(t, l["db"].Equal(c["db"]))
	c["db"].MMDB.Path = "elsewhere"
	require.False(t, l["db"].Equal(c["db"]))
	require.Nil(t, Lookup(nil).Clone())
	require.Nil(t, (*Options)(nil).Clone())

	for _, p := range []string{providers.MMDB, providers.Geofeed, providers.Header} {
		o := &Options{Provider: p}
		o.Initialize(testName)
		require.Equal(t, testName, o.Name)
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		o    *Options
		want error
	}{
		{&Options{}, ErrInvalidName},
		{&Options{Name: reserved.ReferenceNone, Provider: providers.Header}, ErrInvalidName},
		{&Options{Name: testName}, ErrMissingProvider},
		{&Options{Name: testName, Provider: "maxmind"}, ErrInvalidProvider},
		{&Options{Name: testName, Provider: providers.Header, MMDB: mmdbopts.New()}, ErrInvalidBlock},
		{&Options{Name: testName, Provider: providers.MMDB, MMDB: mmdbopts.New(), Geofeed: geofeedopts.New()},
			ErrInvalidBlock},
		{&Options{Name: testName, Provider: providers.Geofeed, Geofeed: geofeedopts.New()}, geofeedopts.ErrNoEntries},
		{&Options{Name: testName, Provider: providers.MMDB, MMDB: mmdbopts.New()}, mmdbopts.ErrNoPath},
	} {
		err := tc.o.Validate()
		require.ErrorIs(t, err, tc.want, "%+v", tc.o)
		if tc.o.Name != "" && tc.want != ErrInvalidName {
			require.ErrorContains(t, err, `geo locator "`+testName+`"`)
		}
	}
	require.ErrorContains(t, (&Options{Name: testName, Provider: "x"}).Validate(), providers.Names())
}
