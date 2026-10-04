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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// the tests' own measurement, shaped like Telegraf's cpu, so live Telegraf data never mixes in
	influxSeedMeasurement = "it_cpu"
	influxSeedTotal       = "cpu-total"
	influxSeedSpan        = 30 * time.Minute
	influxSeedEvery       = 5 * time.Second
	influxSeedPoints      = int(influxSeedSpan / influxSeedEvery)

	influxDB2Write  = "/api/v2/write?org=trickster-dev&bucket=trickster&precision=s"
	influxDB3Write  = "/api/v3/write_lp?db=trickster&precision=second"
	influxSeedCount = `SELECT count("usage_idle") FROM "` + influxSeedMeasurement +
		`" WHERE "cpu" = '` + influxSeedTotal + `' AND time >= '%s' AND time < '%s'`
	influx3SeedCount = "SELECT count(*) AS n FROM " + influxSeedMeasurement +
		" WHERE cpu = '" + influxSeedTotal + "' AND time >= '%s' AND time < '%s'"
)

var influxSeedCPUs = []string{influxSeedTotal, "cpu0", "cpu1"}

func seedInfluxDB2(t *testing.T) time.Time {
	t.Helper()
	end, lines := influxSeedLines()
	req, err := http.NewRequest(http.MethodPost, "http://"+offInfluxDB2Addr+influxDB2Write, strings.NewReader(lines))
	require.NoError(t, err)
	req.Header.Set("Authorization", offInfluxToken)
	requireInfluxWrite(t, req)
	q := url.Values{"db": {offInfluxDB}, "epoch": {"s"}, "q": {influxSeedQuery(influxSeedCount, end)}}
	requireInfluxSeeded(t, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, "http://"+offInfluxDB2Addr+"/query?"+q.Encode(), nil)
		if err == nil {
			req.Header.Set("Authorization", offInfluxToken)
		}
		return req, err
	}, func(b []byte) (float64, error) {
		var doc struct {
			Results []struct {
				Series []struct {
					Values [][]float64 `json:"values"`
				} `json:"series"`
			} `json:"results"`
		}
		err := json.Unmarshal(b, &doc)
		var n float64
		for _, r := range doc.Results {
			for _, s := range r.Series {
				for _, v := range s.Values {
					n += v[len(v)-1]
				}
			}
		}
		return n, err
	})
	return end
}

func seedInfluxDB3(t *testing.T) time.Time {
	t.Helper()
	end, lines := influxSeedLines()
	req, err := http.NewRequest(http.MethodPost, "http://"+offInfluxDB3Addr+influxDB3Write, strings.NewReader(lines))
	require.NoError(t, err)
	requireInfluxWrite(t, req)
	body, err := json.Marshal(map[string]string{"db": offInfluxDB, "q": influxSeedQuery(influx3SeedCount, end)})
	require.NoError(t, err)
	requireInfluxSeeded(t, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, "http://"+offInfluxDB3Addr+"/api/v3/query_sql", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	}, func(b []byte) (float64, error) {
		var rows []struct {
			N float64 `json:"n"`
		}
		err := json.Unmarshal(b, &rows)
		var n float64
		for _, r := range rows {
			n += r.N
		}
		return n, err
	})
	return end
}

func influxSeedLines() (time.Time, string) {
	// every value derives from its timestamp, so a later run rewrites a point it shares with an
	// earlier one unchanged, and whatever an earlier run cached still matches the origin
	end := time.Now().UTC().Truncate(influxSeedEvery)
	var sb strings.Builder
	for at := end.Add(-influxSeedSpan); at.Before(end); at = at.Add(influxSeedEvery) {
		step := at.Unix() / int64(influxSeedEvery/time.Second)
		for i, cpu := range influxSeedCPUs {
			fmt.Fprintf(&sb, "%s,cpu=%s usage_idle=%d.5 %d\n",
				influxSeedMeasurement, cpu, 50+(step+int64(i)*7)%47, at.Unix())
		}
	}
	return end, sb.String()
}

func influxSeedQuery(format string, end time.Time) string {
	return fmt.Sprintf(format, end.Add(-influxSeedSpan).Format(time.RFC3339), end.Format(time.RFC3339))
}

func requireInfluxWrite(t *testing.T, req *http.Request) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	require.Less(t, resp.StatusCode, http.StatusMultipleChoices, "seed write failed: %s", b)
}

func requireInfluxSeeded(t *testing.T, request func() (*http.Request, error), count func([]byte) (float64, error)) {
	t.Helper()
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		req, err := request()
		if !assert.NoError(collect, err) {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if !assert.NoError(collect, err) {
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if !assert.NoError(collect, err) || !assert.Equal(collect, http.StatusOK, resp.StatusCode, "%s", b) {
			return
		}
		n, err := count(b)
		if assert.NoError(collect, err, "%s", b) {
			assert.GreaterOrEqual(collect, n, float64(influxSeedPoints), "seeded points not yet queryable")
		}
	}, 15*time.Second, 200*time.Millisecond, "the seeded points never became queryable")
}
