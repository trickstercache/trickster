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

package integration

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/integration/internal/metricsutil"
	"github.com/trickstercache/trickster/v2/integration/internal/portutil"
	tkconfig "github.com/trickstercache/trickster/v2/pkg/config"
	listenerconfig "github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	tlsoptions "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestClickHouseProtocolMatrix(t *testing.T) {
	h := configHarness(t)
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	flows := []struct {
		name, address, path string
		protocol            clickhouse.Protocol
	}{
		{"http_to_http", h.BaseAddr, "/click1/", clickhouse.HTTP},
		{"native_to_http", h.ClickHouseNativeAddr, "", clickhouse.Native},
		{"http_to_native", h.BaseAddr, "/click-native/", clickhouse.HTTP},
		{"native_to_native", h.ClickHouseNativeOriginAddr, "", clickhouse.Native},
	}
	for _, flow := range flows {
		t.Run(flow.name, func(t *testing.T) {
			options := &clickhouse.Options{
				Addr:     []string{flow.address},
				Protocol: flow.protocol,
				Auth: clickhouse.Auth{
					Database: "default",
					Username: "testauth",
					Password: "trickster",
				},
				Settings: clickhouse.Settings{"max_threads": 1},
			}
			if flow.protocol == clickhouse.HTTP {
				options.HttpUrlPath = flow.path
			} else {
				options.Compression = &clickhouse.Compression{Method: clickhouse.CompressionLZ4}
			}
			db := clickhouse.OpenDB(options)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			verifyClickHouseFlow(t, db)
		})
	}
}

func TestClickHouseNativeListenerCacheMatrix(t *testing.T) {
	h := configHarness(t)
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")
	start, end := clickHouseTripBounds(t)
	const step = int64(5 * 60)
	start = start / step * step
	end = ((end / step) + 1) * step
	mid := start + ((end-start)/step/2)*step
	require.Less(t, mid, end)

	for _, flow := range []struct {
		backend, address string
	}{
		{"click1", h.ClickHouseNativeAddr},
		{"click-native", h.ClickHouseNativeOriginAddr},
	} {
		t.Run(flow.backend, func(t *testing.T) {
			query := func(rangeEnd int64) string {
				return fmt.Sprintf(
					"SELECT toStartOfFiveMinute(pickup_datetime) AS t, count() AS cnt "+
						"FROM trips WHERE pickup_datetime >= toDateTime(%d) AND pickup_datetime < toDateTime(%d) "+
						"GROUP BY t ORDER BY t",
					start, rangeEnd,
				)
			}
			conn, err := clickhouse.Open(&clickhouse.Options{
				Addr:        []string{flow.address},
				Protocol:    clickhouse.Native,
				Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })

			before := metricsutil.ScrapeURL(t, "http://"+h.MetricsAddr+"/metrics", nil)
			runClickHouseNativeCountQuery(t, conn)
			after := metricsutil.ScrapeURL(t, "http://"+h.MetricsAddr+"/metrics", nil)
			requireClickHouseNativeCacheDelta(t, before, after, flow.backend, "kmiss")

			before = after
			runClickHouseNativeCountQuery(t, conn)
			after = metricsutil.ScrapeURL(t, "http://"+h.MetricsAddr+"/metrics", nil)
			requireClickHouseNativeCacheDelta(t, before, after, flow.backend, "hit")

			before = after
			runClickHouseNativeDPCQuery(t, conn, query(mid))
			after = metricsutil.ScrapeURL(t, "http://"+h.MetricsAddr+"/metrics", nil)
			requireClickHouseNativeCacheDelta(t, before, after, flow.backend, "kmiss")

			before = after
			runClickHouseNativeDPCQuery(t, conn, query(end))
			after = metricsutil.ScrapeURL(t, "http://"+h.MetricsAddr+"/metrics", nil)
			requireClickHouseNativeCacheDelta(t, before, after, flow.backend, "phit")
		})
	}
}

func runClickHouseNativeCountQuery(t *testing.T, conn clickhouse.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var count uint64
	require.NoError(t, conn.QueryRow(ctx, "SELECT count() FROM trips").Scan(&count))
	require.Greater(t, count, uint64(0))
}

func runClickHouseNativeDPCQuery(t *testing.T, conn clickhouse.Conn, query string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := conn.Query(ctx, query)
	require.NoError(t, err)
	defer rows.Close()
	var (
		timestamp time.Time
		count     uint64
	)
	for rows.Next() {
		require.NoError(t, rows.Scan(&timestamp, &count))
	}
	require.NoError(t, rows.Err())
}

func requireClickHouseNativeCacheDelta(
	t *testing.T,
	before, after map[string]float64,
	backend, status string,
) {
	t.Helper()
	var delta float64
	deltas := make(map[string]float64)
	for key, value := range after {
		if strings.HasPrefix(key, "trickster_proxy_requests_total{") &&
			strings.Contains(key, `backend_name="`+backend+`"`) &&
			strings.Contains(key, `provider="clickhouse"`) {
			change := value - before[key]
			if change != 0 {
				deltas[key] = change
			}
			if strings.Contains(key, `cache_status="`+status+`"`) {
				delta += change
			}
		}
	}
	require.Equal(t, float64(1), delta, "native cache status %s for %s: %v", status, backend, deltas)
}

func verifyClickHouseFlow(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))
	var (
		user    string
		threads uint64
		count   uint64
	)
	require.NoError(t, db.QueryRowContext(
		ctx, "SELECT currentUser(), getSetting('max_threads'), count() FROM trips",
	).Scan(&user, &threads, &count))
	require.Equal(t, "testauth", user)
	require.Equal(t, uint64(1), threads)
	require.Greater(t, count, uint64(0))
}

func TestClickHouseNativeTLSAndReload(t *testing.T) {
	signal.Reset(syscall.SIGHUP)
	h, address, keyPath, certPath := clickHouseTLSHarness(t)
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:     []string{address},
		Protocol: clickhouse.Native,
		Auth: clickhouse.Auth{
			Database: "default",
			Username: "testauth",
			Password: "trickster",
		},
		Settings:    clickhouse.Settings{"max_threads": 1},
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		TLS:         &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // generated integration certificate
	})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	verifyClickHouseFlow(t, db)
	before, err := clickHouseTLSSerial(address)
	require.NoError(t, err)

	key, cert, err := tlstest.GetTestKeyAndCert(false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(certPath, cert, 0o600))
	require.NoError(t, os.WriteFile(keyPath, key, 0o600))
	data, err := os.ReadFile(h.ConfigPath)
	require.NoError(t, err)
	var c tkconfig.Config
	require.NoError(t, yaml.Unmarshal(data, &c))
	bodyLimit := int64(1 << 20)
	c.Listeners["clickhouse-tls"].MaxRequestBodySizeBytes = &bodyLimit
	data, err = yaml.Marshal(&c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.ConfigPath, data, 0o600))
	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	require.Eventually(t, func() bool {
		serial, err := clickHouseTLSSerial(address)
		return err == nil && serial != before
	}, 10*time.Second, 250*time.Millisecond)

	verifyClickHouseFlow(t, db)
}

func clickHouseTLSHarness(t *testing.T) (tricksterHarness, string, string, string) {
	t.Helper()
	h := configHarness(t)
	ports, releaseTLS := portutil.Reserve(t, 1)
	releaseBase := h.releasePorts
	h.releasePorts = func() {
		releaseBase()
		releaseTLS()
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "clickhouse.key.pem")
	certPath := filepath.Join(dir, "clickhouse.cert.pem")
	require.NoError(t, tlstest.WriteTestKeyAndCert(false, keyPath, certPath))

	data, err := os.ReadFile(h.ConfigPath)
	require.NoError(t, err)
	var c tkconfig.Config
	require.NoError(t, yaml.Unmarshal(data, &c))
	backend := c.Backends["click1"].Clone()
	backend.Name = "click-tls"
	backend.ListenerNames = []string{"clickhouse-tls"}
	backend.RequireTLS = true
	backend.TLS = tlsoptions.New()
	backend.TLS.ServeTLS = true
	backend.TLS.FullChainCertPath = certPath
	backend.TLS.PrivateKeyPath = keyPath
	c.Backends["click-tls"] = backend
	c.Listeners["clickhouse-tls"] = &listenerconfig.Options{
		Protocol:         listenerconfig.ProtocolClickHouse,
		ListenAddress:    "127.0.0.1",
		ListenPort:       ports[0],
		TLSWatchInterval: timeconv.Duration(100 * time.Millisecond),
	}
	data, err = yaml.Marshal(&c)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(h.ConfigPath, data, 0o600))
	return h, "127.0.0.1:" + strconv.Itoa(ports[0]), keyPath, certPath
}

func clickHouseTLSSerial(address string) (string, error) {
	conn, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // generated integration certificate
	if err != nil {
		return "", err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.String(), nil
}

func TestClickHouseNativeLimitations(t *testing.T) {
	h := configHarness(t)
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{h.ClickHouseNativeAddr},
		Protocol:    clickhouse.Native,
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, query := range []string{"USE default", "SET max_threads = 1"} {
		err := conn.Exec(ctx, query)
		require.Error(t, err)
		require.Contains(t, err.Error(), "session-changing")
	}

	_, err = conn.PrepareBatch(ctx, "INSERT INTO trips (passenger_count)")
	require.Error(t, err)
	require.Contains(t, err.Error(), "INSERT queries are not supported")

	table, err := ext.NewTable("external_limits", ext.Column("value", "UInt8"))
	require.NoError(t, err)
	require.NoError(t, table.Append(uint8(1)))
	externalContext := clickhouse.Context(ctx, clickhouse.WithExternalTable(table))
	_, err = conn.Query(externalContext, "SELECT * FROM external_limits")
	require.Error(t, err)
	require.Contains(t, err.Error(), "external tables")

	resp, body := h.do(t, "/click-native/", withParams(url.Values{
		"query": {"SELECT 1 FORMAT Parquet"},
	}))
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), "unsupported native upstream output format")
}

func TestClickHouseNativeSDK(t *testing.T) {
	h := configHarness(t)
	clickAddr := h.BaseAddr
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:        []string{clickAddr},
		Protocol:    clickhouse.HTTP,
		HttpUrlPath: "/click1/",
	})
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	t.Run("ping", func(t *testing.T) {
		require.NoError(t, db.PingContext(ctx))
	})

	t.Run("server_hello", func(t *testing.T) {
		var cnt uint64
		require.NoError(t, db.QueryRowContext(ctx, "SELECT count() FROM trips").Scan(&cnt))
		require.Greater(t, cnt, uint64(0))
		t.Logf("count: %d", cnt)
	})

	t.Run("select_typed", func(t *testing.T) {
		rows, err := db.QueryContext(ctx, "SELECT pickup_datetime, passenger_count, trip_distance FROM trips WHERE pickup_datetime > now() - INTERVAL 1 YEAR ORDER BY pickup_datetime LIMIT 5")
		require.NoError(t, err)
		defer rows.Close()

		var count int
		for rows.Next() {
			var dt time.Time
			var passengers uint8
			var distance float32
			require.NoError(t, rows.Scan(&dt, &passengers, &distance))
			count++
		}
		require.NoError(t, rows.Err())
		require.Greater(t, count, 0)
		t.Logf("%d typed rows", count)
	})
}

// TestClickHouseNativeProtocolListener tests Flow 1: client speaks native
// protocol to Trickster's protocol listener, which proxies through the
// caching engine to ClickHouse's HTTP port.
func TestClickHouseNativeProtocolListener(t *testing.T) {
	h := configHarness(t)
	h.start(t)
	waitForClickHouseData(t, "127.0.0.1:8123")

	// Connect via native protocol to Trickster's protocol listener
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:     []string{h.ClickHouseNativeAddr},
		Protocol: clickhouse.Native,
	})
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	t.Run("ping", func(t *testing.T) {
		require.NoError(t, conn.Ping(ctx))
	})

	t.Run("select", func(t *testing.T) {
		rows, err := conn.Query(ctx, "SELECT count() FROM trips")
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
	})

	t.Run("nullable", func(t *testing.T) {
		rows, err := conn.Query(ctx, "SELECT toNullable(1) AS n, CAST(NULL AS Nullable(UInt8)) AS m")
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
	})

	t.Run("array", func(t *testing.T) {
		rows, err := conn.Query(ctx, "SELECT [1,2,3] AS arr")
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
	})

	t.Run("tuple", func(t *testing.T) {
		rows, err := conn.Query(ctx, "SELECT tuple(1, 'hello') AS t")
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
	})

	t.Run("map", func(t *testing.T) {
		rows, err := conn.Query(ctx, "SELECT map('key', 1) AS m")
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
	})

	t.Run("cache_hit", func(t *testing.T) {
		q := "SELECT count() FROM trips"
		rows1, err := conn.Query(ctx, q)
		require.NoError(t, err)
		rows1.Close()

		rows2, err := conn.Query(ctx, q)
		require.NoError(t, err)
		defer rows2.Close()
		require.True(t, rows2.Next())
	})
}
