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

package questdb

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/testutil/sqlcompat"
)

const corpusPath = "testdata/compatibility/v1.json"

func corpusAnalyze(_, sql string) sqlanalyzer.Analysis {
	return analyzer.Analyze(sql, time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))
}

func TestCompatibilityCorpus(t *testing.T) {
	// QuestDB's pgwire TIMESTAMP text is precise to microseconds.
	sqlcompat.Run(t, corpusPath, corpusAnalyze, time.Microsecond)
}

func BenchmarkCompatibilityCorpus(b *testing.B) {
	sqlcompat.Benchmark(b, corpusPath, corpusAnalyze)
}
