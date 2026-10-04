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

// Package geodb builds MaxMind DB format files for tests, so no binary database is committed.
package geodb

import (
	"bytes"
	"net"
	"os"
	"testing"
	"time"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

// database types, as the vendors name them
const (
	TypeGeoLite2Country = "GeoLite2-Country"
	TypeGeoLite2City    = "GeoLite2-City"
	TypeIPinfoLite      = "ipinfo lite"
	TypeCustom          = "Example-Custom"
)

// Options shape a test database
type Options struct {
	DatabaseType string
	// IPVersion is 4 or 6; zero is 6
	IPVersion int
	// Built is the database's build time; zero is now
	Built time.Time
	// Records maps networks, in CIDR notation, to their records
	Records map[string]mmdbtype.Map
}

// GeoIP2 returns a record in MaxMind's layout; empty values are left out
func GeoIP2(country, continent, subdivision string) mmdbtype.Map {
	m := mmdbtype.Map{}
	if country != "" {
		m["country"] = mmdbtype.Map{"iso_code": mmdbtype.String(country)}
	}
	if continent != "" {
		m["continent"] = mmdbtype.Map{"code": mmdbtype.String(continent)}
	}
	if subdivision != "" {
		m["subdivisions"] = mmdbtype.Slice{mmdbtype.Map{"iso_code": mmdbtype.String(subdivision)}}
	}
	return m
}

// IPinfo returns a record in IPinfo Lite's flat layout
func IPinfo(country, continent string) mmdbtype.Map {
	return mmdbtype.Map{
		"country_code":   mmdbtype.String(country),
		"country":        mmdbtype.String("name of " + country),
		"continent_code": mmdbtype.String(continent),
		"asn":            mmdbtype.String("AS64496"),
	}
}

// Bytes returns the database the options describe
func Bytes(t testing.TB, o Options) []byte {
	t.Helper()
	built := o.Built
	if built.IsZero() {
		built = time.Now()
	}
	ipVersion := o.IPVersion
	if ipVersion == 0 {
		ipVersion = 6
	}
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType:            o.DatabaseType,
		IPVersion:               ipVersion,
		RecordSize:              24,
		BuildEpoch:              built.Unix(),
		IncludeReservedNetworks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for network, record := range o.Records {
		_, n, err := net.ParseCIDR(network)
		if err != nil {
			t.Fatal(err)
		}
		if err := tree.Insert(n, record); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if _, err := tree.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Write writes the database the options describe to path, by writing a new file and renaming it over path
func Write(t testing.TB, path string, o Options) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, Bytes(t, o), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}
