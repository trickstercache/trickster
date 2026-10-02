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

package prometheus

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"

	"github.com/stretchr/testify/require"
)

// liveCloudWatch skips unless TRICKSTER_AWS_TEST=1 and TRICKSTER_CW_METRIC names a PromQL-visible
// metric; it configures a cloudwatch backend from the credential chain.
func liveCloudWatch(t *testing.T) (metric string, configure func(*bo.Options)) {
	t.Helper()
	if os.Getenv("TRICKSTER_AWS_TEST") != "1" {
		t.Skip("set TRICKSTER_AWS_TEST=1 to run against a real AWS account")
	}
	metric = os.Getenv("TRICKSTER_CW_METRIC")
	if metric == "" {
		t.Skip("set TRICKSTER_CW_METRIC to a metric name visible to CloudWatch PromQL")
	}
	region := os.Getenv("TRICKSTER_AWS_REGION")
	if region == "" {
		region = "us-east-1"
	}
	return metric, func(o *bo.Options) {
		o.Provider = "prometheus"
		o.OriginURL = ""
		o.Prometheus = &po.Options{Flavor: po.FlavorCloudWatch}
		o.SigV4 = &taws.Options{Region: region, Profile: os.Getenv("TRICKSTER_AWS_PROFILE")}
		require.NoError(t, o.Initialize("default"))
		_, err := o.Validate()
		require.NoError(t, err)
	}
}

func TestLiveCloudWatchRangeIsCached(t *testing.T) {
	metric, configure := liveCloudWatch(t)
	end := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	serve := rangeServer(t, `sum({"`+metric+`"})`, end, configure)
	for _, want := range []string{"kmiss", "hit"} {
		resp := serve(1)
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		require.Contains(t, resp.Header.Get(headers.NameTricksterResult), "status="+want)
		require.Contains(t, string(body), `"resultType":"matrix"`)
	}
}

// The query relabels one metric into three series, which limit=2 truncates.
func TestLiveCloudWatchTruncationIsProxied(t *testing.T) {
	metric, configure := liveCloudWatch(t)
	end := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	sel := `{"` + metric + `"}`
	query := strings.Join([]string{
		sel,
		`label_replace(` + sel + `, "trickster_live", "a", "", "")`,
		`label_replace(` + sel + `, "trickster_live", "b", "", "")`,
	}, " or ")
	serve := rangeServer(t, query, end, configure)
	resp := serve(1)
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), "truncated")
	require.Contains(t, resp.Header.Get(headers.NameTricksterResult), "status=proxy-only")
}

func TestLiveCloudWatchHealthProbe(t *testing.T) {
	_, configure := liveCloudWatch(t)
	o := bo.New()
	configure(o)
	b, err := NewClient("default", o, nil, nil, nil, nil)
	require.NoError(t, err)
	c := b.(*Client)
	hc := c.DefaultHealthCheckConfig()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		hc.Scheme+"://"+hc.Host+hc.Path+"?"+hc.Query, nil)
	require.NoError(t, err)
	resp, err := c.HTTPClient().Do(r)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Contains(t, string(body), `"status":"success"`)
}
