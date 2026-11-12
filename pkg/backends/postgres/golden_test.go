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

package postgres

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/proxy/pgwire"
	"github.com/trickstercache/trickster/v2/pkg/testutil/sqlgolden"
)

func goldenAnalyze(utc bool) sqlgolden.Analyze {
	analyzer := Engine().Analyzer().(pgwire.SessionAnalyzer).ForSession(pgwire.SessionView{UTC: utc})
	return func(sql string, now time.Time) sqlanalyzer.Analysis { return analyzer.Analyze(sql, now) }
}

func TestGoldenUTC(t *testing.T) { sqlgolden.RunAnalyzer(t, "utc", goldenAnalyze(true)) }

func TestGoldenZoned(t *testing.T) { sqlgolden.RunAnalyzer(t, "zoned", goldenAnalyze(false)) }
