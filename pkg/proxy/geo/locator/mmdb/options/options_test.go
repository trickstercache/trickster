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
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestSchema(t *testing.T) {
	for name, want := range map[string]Schema{
		"": 0, "auto": SchemaAuto, " GeoIP2 ": SchemaGeoIP2, "IPINFO": SchemaIPinfo, "custom": SchemaCustom,
	} {
		got, err := ParseSchema(name)
		require.NoError(t, err, name)
		require.Equal(t, want, got, name)
	}
	_, err := ParseSchema("maxmind")
	require.ErrorIs(t, err, ErrInvalidSchema)
	require.ErrorContains(t, err, "auto, geoip2, ipinfo, custom")
	require.Empty(t, Schema(0).String())
	require.Empty(t, Schema(99).String())

	var o Options
	require.NoError(t, yaml.Unmarshal([]byte("schema: GEOIP2\nfields:\n  subdivision: [subdivisions, 0, iso_code]\n"), &o))
	require.Equal(t, SchemaGeoIP2, o.Schema)
	require.Equal(t, []string{"subdivisions", "0", "iso_code"}, o.Fields.Subdivision)
	out, err := yaml.Marshal(&Options{Schema: SchemaIPinfo})
	require.NoError(t, err)
	require.Equal(t, "schema: ipinfo\n", string(out))
	out, err = yaml.Marshal(&Options{Path: "x"})
	require.NoError(t, err)
	require.Equal(t, "path: x\n", string(out))
	require.Error(t, yaml.Unmarshal([]byte("schema: nope\n"), &o))
}

func TestOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.mmdb")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	o := New()
	require.ErrorIs(t, o.Validate(), ErrNoPath)
	o.Initialize()
	require.Equal(t, timeconv.Duration(providers.DefaultReloadInterval), o.ReloadInterval)
	o.Path = path + ".missing"
	require.ErrorIs(t, o.Validate(), os.ErrNotExist)
	o.Path = path
	require.NoError(t, o.Validate())

	o.Fields = &Fields{Country: []string{"cc"}}
	require.ErrorIs(t, o.Validate(), ErrFieldsNotCustom)
	o.Schema = SchemaCustom
	require.NoError(t, o.Validate())
	o.Fields = &Fields{Continent: []string{"cont"}}
	require.ErrorIs(t, o.Validate(), ErrNoCountryField)
	o.Fields = nil
	require.ErrorIs(t, o.Validate(), ErrNoCountryField)
	o.Schema = 0
	o.MaxAge = -1
	require.ErrorIs(t, o.Validate(), ErrNegativeMaxAge)
	o.MaxAge = 0
	o.ReloadInterval = timeconv.Duration(time.Second)
	require.ErrorIs(t, o.Validate(), providers.ErrInvalidReloadInterval)

	o.Fields = &Fields{Country: []string{"a"}, Continent: []string{"b"}, Subdivision: []string{"c"}}
	c := o.Clone()
	require.Equal(t, o, c)
	c.Fields.Country[0] = "changed"
	require.Equal(t, "a", o.Fields.Country[0])
	require.Nil(t, (*Options)(nil).Clone())
}
