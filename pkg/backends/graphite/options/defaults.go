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

package options

import "time"

// DefaultTimeZone is the time zone assumed for date-anchored from/until
// values (midnight, today, MM/DD/YY, ...) when the request has no tz
// parameter. It should match the origin's graphite-web TIME_ZONE setting.
const DefaultTimeZone = "UTC"

// Resolution registry defaults
const (
	// DefaultRegistryTTL is how long a learned ladder is trusted. Whisper
	// ladders only change by operator action (whisper-resize.py), so this is
	// long; a misprediction bumps the registry generation regardless.
	DefaultRegistryTTL = 24 * time.Hour
	// DefaultNegativeTTL is the initial backoff after a failed resolution;
	// it doubles per consecutive failure up to DefaultNegativeTTLMax
	DefaultNegativeTTL    = 30 * time.Second
	DefaultNegativeTTLMax = 10 * time.Minute
	// DefaultMaxEntries bounds each registry layer
	DefaultMaxEntries = 100000
	// DefaultProbeConcurrency caps concurrent ladder-learning runs per backend
	DefaultProbeConcurrency = 2
	// DefaultProbeBudget caps the probes one learning run may issue
	DefaultProbeBudget = 96
	// DefaultFindCacheTTL is how long a wildcard expansion is reused
	DefaultFindCacheTTL = time.Minute
)

// Sizing defaults. Graphite needs its own because the delta cache works at
// the origin's native resolution: maxDataPoints is stripped upstream
// (decision D3), so Trickster buffers and caches every point Whisper holds
// for the requested window, not the few hundred a dashboard draws. The
// generic backend defaults (512 KB, 1024 points) are sized for origins that
// consolidate before responding, and would reject or crop ordinary Graphite
// dashboard panels. Both are applied by
// backends/options.GetProviderDefaults when the configuration is silent.
const (
	// DefaultMaxObjectSizeBytes is 64 MiB, which holds the widest panel the
	// developer environment draws (120 days over two series on a 5-minute
	// archive is ~1.6 MB of JSON) with room for wildcards that fan out to
	// dozens of series on a finer archive.
	DefaultMaxObjectSizeBytes = 64 * 1024 * 1024
	// DefaultTimeseriesRetentionFactor is 524288 points per series, which
	// covers 5 years at a 5-minute step or 60 days at a 10-second one. The
	// generic default of 1024 is only about 3.5 days at 5 minutes, so a
	// wide panel would be cropped and refetched on every refresh.
	DefaultTimeseriesRetentionFactor = 524288
)

const (
	// DefaultMaxTargetsPerRequest bounds how many target parameters one
	// render request may carry and still be considered for acceleration. A
	// large Grafana dashboard panel sends a few dozen.
	DefaultMaxTargetsPerRequest = 128
	// DefaultMaxTargetLength bounds one target expression's length in
	// bytes. Real expressions, wildcards included, are a few hundred bytes.
	DefaultMaxTargetLength = 16384
	// DefaultMaxExpandedLeaves bounds one wildcard expansion's leaf count:
	// a dashboard wildcard resolves to at most a few thousand series in
	// any deployment acceleration usefully serves.
	DefaultMaxExpandedLeaves = 4096
	// DefaultMaxExpansionBytes bounds one expansion's aggregate decoded
	// leaf-name bytes.
	DefaultMaxExpansionBytes = 2 * 1024 * 1024
)
