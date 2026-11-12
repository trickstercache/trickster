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

package sqlgolden

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/testutil/golden"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

type fixedRenderer struct{ fail bool }

func (r fixedRenderer) RenderExtent(e timeseries.Extent) (string, error) {
	if r.fail {
		return "", errors.New("unrenderable")
	}
	return e.Start.UTC().Format(time.RFC3339), nil
}

func fakeAnalyze(sql string, now time.Time) sqlanalyzer.Analysis {
	switch {
	case strings.HasPrefix(sql, "SELECT date_bin"):
		return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeDelta, Plan: &sqlanalyzer.QueryPlan{
			CanonicalSQL: sql, Step: time.Minute, LowerBound: &sqlanalyzer.Bound{Value: now, Inclusive: true},
			Renderer: fixedRenderer{fail: strings.Contains(sql, "host IN")},
		}}
	case strings.HasPrefix(sql, "SELECT"):
		return sqlanalyzer.Analysis{Mode: sqlanalyzer.CacheModeObject, Plan: &sqlanalyzer.QueryPlan{}}
	}
	return sqlanalyzer.Analysis{Err: errors.New("unsupported")}
}

func TestGoldenRoundTrip(t *testing.T) {
	t.Chdir(t.TempDir())
	update := *golden.Update
	t.Cleanup(func() { *golden.Update = update })
	*golden.Update = true
	RunAnalyzer(t, "fake", fakeAnalyze)
	RunText(t, "text", strings.ToUpper)
	*golden.Update = false
	RunAnalyzer(t, "fake", fakeAnalyze)
	RunText(t, "text", strings.ToUpper)
	if n := len(Statements(t)); n == 0 {
		t.Fatal("empty corpus")
	}
}

func TestGoldenReportsDifferences(t *testing.T) {
	t.Chdir(t.TempDir())
	update := *golden.Update
	t.Cleanup(func() { *golden.Update = update })
	*golden.Update = true
	RunText(t, "text", strings.ToUpper)
	*golden.Update = false
	for name, run := range map[string]func(*testing.T){
		"changed output": func(t *testing.T) { RunText(t, "text", strings.ToLower) },
		"missing golden": func(t *testing.T) { RunText(t, "absent", strings.ToUpper) },
	} {
		if ok := testing.RunTests(func(_, _ string) (bool, error) { return true, nil },
			[]testing.InternalTest{{Name: name, F: run}}); ok {
			t.Errorf("%s: the comparison passed", name)
		}
	}
}
