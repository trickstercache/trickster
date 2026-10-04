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

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	geolocopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"

	"github.com/ClickHouse/clickhouse-go/v2"
	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

const (
	geoNativeACL = "geo-it-native"
	// ClickHouse's IP_ADDRESS_NOT_ALLOWED and MySQL's ER_HOST_NOT_PRIVILEGED
	clickHouseIPAddressNotAllowed = 195
	mysqlHostNotPrivileged        = 1130
)

func geoNativeConfig(c *tkconfig.Config) {
	// the test client connects from loopback, which the geofeed places in France, and the geo ACL denies
	c.GeoLocators = geolocopts.Lookup{geolocopts.DefaultName: {
		Provider: providers.Geofeed,
		Geofeed:  &geofeedopts.Options{Entries: []string{"127.0.0.1/32,FR", "::1/128,FR"}},
	}}
	c.GeoACLs = geoaclopts.Lookup{geoNativeACL: {Deny: []string{"FR"}}}
	for _, name := range []string{"mysql1", "click1"} {
		if b := c.Backends[name]; b != nil {
			b.GeoACLName = geoNativeACL
		}
	}
}

func TestGeoACLNative(t *testing.T) {
	if testing.Short() {
		t.Skip("starts Trickster; skipping in -short mode")
	}
	h := configHarness(t, geoNativeConfig)
	h.start(t)
	denied := func() float64 {
		v, _ := metricValue(t, h.MetricsAddr, geoDecisions, `geo_acl="`+geoNativeACL+`",plane="native",verdict="deny"`)
		return v
	}
	before := denied()

	t.Run("mysql", func(t *testing.T) {
		config := gomysql.NewConfig()
		config.User, config.Passwd = mysqlIntegrationUser, "not-the-password"
		config.Net, config.Addr, config.Timeout = "tcp", h.MySQLAddr, 5*time.Second
		connector, err := gomysql.NewConnector(config)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = connector.Connect(ctx)
		var mysqlErr *gomysql.MySQLError
		require.True(t, errors.As(err, &mysqlErr), "%v", err)
		require.Equal(t, uint16(mysqlHostNotPrivileged), mysqlErr.Number)
		require.Contains(t, mysqlErr.Message, geoaclopts.DefaultMessage)
	})

	t.Run("clickhouse", func(t *testing.T) {
		db := clickhouse.OpenDB(&clickhouse.Options{
			Addr: []string{h.ClickHouseNativeAddr}, Protocol: clickhouse.Native,
			Auth: clickhouse.Auth{Database: "default", Username: "testauth", Password: "trickster"},
		})
		t.Cleanup(func() { _ = db.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var exception *clickhouse.Exception
		err := db.PingContext(ctx)
		require.True(t, errors.As(err, &exception), "%v", err)
		require.Equal(t, int32(clickHouseIPAddressNotAllowed), exception.Code)
		require.Equal(t, geoaclopts.DefaultMessage, exception.Message)
	})

	require.Eventually(t, func() bool { return denied() >= before+2 }, 10*time.Second, 50*time.Millisecond)
}
