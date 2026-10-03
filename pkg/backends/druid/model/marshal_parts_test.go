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

package model

import (
	"bytes"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/parsing/sqlanalyzer"
	"github.com/trickstercache/trickster/v2/pkg/testutil/parts"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestMarshalReadsSeriesParts(t *testing.T) {
	view := parts.Of(benchDruidDataSet(4, 6), 60e9)
	if !view.HasParts() {
		t.Fatal("the view has no parts")
	}
	sqlPlan := &SQLQueryPlan{Plan: &sqlanalyzer.QueryPlan{
		OutputColumn: "__time",
		GroupColumns: []string{"page"}, ValueColumns: []string{"count", "added"},
	}}
	for _, plan := range []any{
		&QueryPlan{queryType: queryTimeseries}, &QueryPlan{queryType: queryGroupBy, descending: true},
		&QueryPlan{queryType: queryTopN}, sqlPlan,
		&SQLQueryPlan{Plan: sqlPlan.Plan, format: SQLResponseArray, header: true},
	} {
		rlo := &timeseries.RequestOptions{ProviderRequest: plan}
		var got, want bytes.Buffer
		if err := MarshalTimeseriesWriter(view, rlo, 200, &got); err != nil {
			t.Fatal(err)
		}
		if err := MarshalTimeseriesWriter(view.Flat(), rlo, 200, &want); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) || got.Len() < 10 {
			t.Fatalf("%T:\n got %s\nwant %s", plan, got.Bytes(), want.Bytes())
		}
	}
}
