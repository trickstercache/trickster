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
	"net/url"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	po "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
)

const (
	handlerUnsupported = "unsupported"
	// upLimit caps the series a query returns, so it changes the result and is cache identity
	upLimit = "limit"
	// cloudWatchHealthQuery selects no metric, so the probe scans no billable samples
	cloudWatchHealthQuery = "query=vector(1)"
)

// cloudWatchPaths are the only routes the CloudWatch PromQL API serves.
var cloudWatchPaths = []string{
	APIPath + mnQueryRange,
	APIPath + mnQuery,
	APIPath + mnSeries,
	APIPath + mnLabels,
	APIPath + mnLabel + "/",
}

// ampPaths are the read routes Amazon Managed Service for Prometheus serves; remote write is not one.
var ampPaths = []string{
	APIPath + mnQueryRange,
	APIPath + mnQuery,
	APIPath + mnSeries,
	APIPath + mnLabels,
	APIPath + mnLabel + "/",
	APIPath + mnMetadata,
	APIPath + mnRules,
	APIPath + mnAlerts,
}

// flavorHooks returns the hooks for the backend's prometheus flavor; no flavor yields none.
func flavorHooks(o *bo.Options) Hooks {
	if o == nil || o.Prometheus == nil {
		return Hooks{}
	}
	switch o.Prometheus.Flavor {
	case po.FlavorCloudWatch:
		return Hooks{
			AllowedPaths:      cloudWatchPaths,
			CacheKeyParams:    []string{upLimit},
			HealthCheckConfig: cloudWatchHealthCheck,
			MaxSeries:         po.CloudWatchMaxSeries,
		}
	case po.FlavorAMP:
		return Hooks{AllowedPaths: ampPaths}
	}
	return Hooks{}
}

// cloudWatchHealthCheck probes the instant query API with an expression that reads no metric.
func cloudWatchHealthCheck(u *url.URL) *ho.Options {
	o := ho.New()
	o.Scheme = u.Scheme
	o.Host = u.Host
	o.Path = u.Path + APIPath + mnQuery
	o.Query = cloudWatchHealthQuery
	return o
}
