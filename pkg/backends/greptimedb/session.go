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

package greptimedb

import "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"

var sessionSettings = pgwire.SessionSettings{
	Tracked: map[string]struct{}{
		"timezone": {}, "datestyle": {}, "intervalstyle": {}, "bytea_output": {}, "search_path": {},
	},
	Neutral: map[string]struct{}{
		"application_name": {}, "statement_timeout": {}, "client_encoding": {},
		// Accepted as no-ops; float output and string parsing are not configurable this way.
		"extra_float_digits": {}, "standard_conforming_strings": {},
	},
	Aliases:       map[string]string{"time_zone": "timezone"},
	LocalPersists: true, UnconfirmedStartup: true,
}

func (engine) SessionDefaultsProbe() pgwire.SessionDefaultsProbe {
	return pgwire.SessionDefaultsProbe{
		SQL:   "SHOW TIMEZONE; SHOW DateStyle; SHOW IntervalStyle",
		Names: []string{"timezone", "datestyle", "intervalstyle"},
	}
}

func (engine) SessionSettings() pgwire.SessionSettings { return sessionSettings }

func (engine) TimeSemantics() pgwire.TimeSemantics {
	return pgwire.TimeSemantics{
		NaiveTimestampsAreUTC: true, LosslessFloatText: true,
		AssumedDateStyle: "ISO, MDY", AssumedIntervalStyle: "postgres",
		AssumedStandardConformingStrings: "on", AssumedIntegerDatetimes: "on",
	}
}
