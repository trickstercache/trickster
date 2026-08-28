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

package registry

import (
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse"
	"github.com/trickstercache/trickster/v2/pkg/backends/graphite"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb"
	"github.com/trickstercache/trickster/v2/pkg/backends/mysql"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxy"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener/native"
)

func SupportedProviders() types.Lookup {
	return types.Lookup{
		providers.ALB:                    alb.NewClient,
		providers.ClickHouse:             clickhouse.NewClient,
		providers.Graphite:               graphite.NewClient,
		providers.InfluxDB:               influxdb.NewClient,
		providers.MySQL:                  mysql.NewClient,
		providers.Prometheus:             prometheus.NewClient,
		providers.Rule:                   rule.NewClient,
		providers.Proxy:                  reverseproxy.NewClient,
		providers.ReverseProxyShort:      reverseproxy.NewClient,
		providers.ReverseProxy:           reverseproxy.NewClient,
		providers.ReverseProxyCacheShort: reverseproxycache.NewClient,
		providers.ReverseProxyCache:      reverseproxycache.NewClient,
	}
}

// NativeListeners returns the provider-owned adapters for non-HTTP listener
// protocols. Adding another native protocol requires registration here, not a
// protocol branch in daemon setup or configuration validation.
var nativeListeners = func() native.Registry {
	mysqlAdapter := mysql.NativeListenerAdapter()
	clickhouseAdapter := clickhouse.NativeListenerAdapter()
	return native.Registry{mysqlAdapter.Protocol(): mysqlAdapter, clickhouseAdapter.Protocol(): clickhouseAdapter}
}()

func NativeListeners() native.Registry {
	return nativeListeners
}
