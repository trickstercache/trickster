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

package registration

import (
	"github.com/trickstercache/trickster/v2/pkg/backends/alb"
	"github.com/trickstercache/trickster/v2/pkg/backends/clickhouse"
	"github.com/trickstercache/trickster/v2/pkg/backends/influxdb"
	"github.com/trickstercache/trickster/v2/pkg/backends/irondb"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers/registration/types"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxy"
	"github.com/trickstercache/trickster/v2/pkg/backends/reverseproxycache"
	"github.com/trickstercache/trickster/v2/pkg/backends/rule"
)

func SupportedProviders() types.Lookup {
	return types.Lookup{
		"alb":               alb.NewClient,
		"clickhouse":        clickhouse.NewClient,
		"influxdb":          influxdb.NewClient,
		"irondb":            irondb.NewClient,
		"prometheus":        prometheus.NewClient,
		"rp":                reverseproxy.NewClient,
		"proxy":             reverseproxy.NewClient,
		"reverseproxy":      reverseproxy.NewClient,
		"rpc":               reverseproxycache.NewClient,
		"reverseproxycache": reverseproxycache.NewClient,
		"rule":              rule.NewClient,
	}
}
