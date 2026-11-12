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

package pgfamily

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/pgparser/ast"
	"github.com/trickstercache/trickster/v2/pkg/testutil/sqlgolden"
)

const parseFailed = "parse error"

func TestGoldenDataFusion(t *testing.T) {
	sqlgolden.RunAnalyzer(t, "datafusion", newDataFusionAnalyzer().Analyze)
}

func TestGoldenStrict(t *testing.T) {
	sqlgolden.RunAnalyzer(t, "strict", newStrictAnalyzer().Analyze)
}

func TestGoldenParse(t *testing.T) {
	sqlgolden.RunText(t, "parse", func(sql string) string {
		parsed, err := ParseOne(sql)
		if err != nil {
			return parseFailed
		}
		return ast.AsString(parsed)
	})
}
