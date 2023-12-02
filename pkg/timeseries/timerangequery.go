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

//go:generate msgp

package timeseries

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/urls"
)

// TimeRangeQuery represents a timeseries database query parsed from an inbound HTTP request
type TimeRangeQuery struct {
	// Statement is the timeseries database query (with tokenized timeranges where present) requested by the user
	Statement string `msg:"stmt"`
	// Extent provides the start and end times for the request from a timeseries database
	Extent Extent `msg:"ex"`
	// Step indicates the amount of time in seconds between each datapoint in a TimeRangeQuery's resulting timeseries
	Step time.Duration `msg:"-"`
	// TemplateURL is used by some Backend providers for templatization of url parameters containing timestamps
	TemplateURL *url.URL `msg:"-"`
	// IsOffset is true if the query uses a relative offset modifier
	IsOffset bool `msg:"-"`
	// StepNS is the nanosecond representation for Step
	StepNS int64 `msg:"step"`
	// BackfillTolerance can be updated to override the overall backfill tolerance per query
	BackfillTolerance time.Duration `msg:"-"`
	// BackfillToleranceNS is the nanosecond representation for BackfillTolerance
	BackfillToleranceNS int64 `msg:"bft"`
	// RecordLimit is the LIMIT value of the query
	RecordLimit int `msg:"rl"`
	// TimestampDefinition sets the definition for the Timestamp column in the in the timeseries based on the query
	TimestampDefinition FieldDefinition `msg:"tsdef"`
	// TagFieldDefinitions contains the definitions for Tag columns in the timeseries, based on the query
	TagFieldDefintions []FieldDefinition `msg:"tfdefs"`
	// ValueFieldDefinitions contains the definitions for Value columns in the timeseries, based on the query
	ValueFieldDefinitions []FieldDefinition `msg:"vfdefs"`
	// ParsedQuery is a member for the vendor-specific query object
	ParsedQuery interface{} `msg:"-"`
}

// Clone returns an exact copy of a TimeRangeQuery
func (trq *TimeRangeQuery) Clone() *TimeRangeQuery {
	t := &TimeRangeQuery{
		Statement:           trq.Statement,
		Step:                trq.Step,
		StepNS:              trq.StepNS,
		Extent:              Extent{Start: trq.Extent.Start, End: trq.Extent.End},
		IsOffset:            trq.IsOffset,
		TimestampDefinition: trq.TimestampDefinition.Clone(),
	}

	if trq.TagFieldDefintions != nil {
		t.TagFieldDefintions = make([]FieldDefinition, len(trq.TagFieldDefintions))
		copy(t.TagFieldDefintions, trq.TagFieldDefintions)
	}

	if trq.ValueFieldDefinitions != nil {
		t.ValueFieldDefinitions = make([]FieldDefinition, len(trq.ValueFieldDefinitions))
		copy(t.ValueFieldDefinitions, trq.ValueFieldDefinitions)
	}

	if trq.TemplateURL != nil {
		t.TemplateURL = urls.Clone(trq.TemplateURL)
	}

	return t
}

// NormalizeExtent adjusts the Start and End of a TimeRangeQuery's Extent to align against normalized boundaries.
func (trq *TimeRangeQuery) NormalizeExtent() {
	if trq.Step > 0 {
		if !trq.IsOffset && trq.Extent.End.After(time.Now()) {
			trq.Extent.End = time.Now()
		}
		trq.Extent.Start = trq.Extent.Start.Truncate(trq.Step)
		trq.Extent.End = trq.Extent.End.Truncate(trq.Step)
	}
}

func (trq *TimeRangeQuery) String() string {
	return fmt.Sprintf(`{ "statement": "%s", "step": "%s", "extent": "%s", "tsd": "%s", "td": %s, "vd": %s }`,
		strings.Replace(trq.Statement, `"`, `\"`, -1), trq.Step.String(),
		trq.Extent.String(), trq.TimestampDefinition.String(),
		FieldDefinitions(trq.TagFieldDefintions).String(), FieldDefinitions(trq.ValueFieldDefinitions))
}

// GetBackfillTolerance will return the backfill tolerance for the query based on the provided
// defaults, and any query-specific tolerance directives included in the query comments
func (trq *TimeRangeQuery) GetBackfillTolerance(def time.Duration, points int) time.Duration {
	if trq.BackfillTolerance > 0 {
		return trq.BackfillTolerance
	}
	if trq.BackfillTolerance < 0 {
		return 0
	}

	if points > 0 {
		sd := time.Duration(points) * trq.Step
		if sd > def {
			return sd
		}
	}

	return def
}

// Size returns the memory usage in bytes of the TimeRangeQuery
func (trq *TimeRangeQuery) Size() int {
	return len(trq.Statement) + 24 + 8 + trq.TimestampDefinition.Size() + // Extent=24 + Step=8
		urls.Size(trq.TemplateURL) + 11 // FFwDisable=1 IsOffset=1 StepNS=8 CustomData=1
}

// ExtractBackfillTolerance will look for the BackfillToleranceFlag in the provided string
// and return the BackfillTolerance value if present
func (trq *TimeRangeQuery) ExtractBackfillTolerance(input string) {
	if x := strings.Index(input, BackfillToleranceFlag); x > 1 {
		x += 29
		y := x
		for ; y < len(input); y++ {
			if input[y] < 48 || input[y] > 57 {
				break
			}
		}
		if i, err := strconv.Atoi(input[x:y]); err == nil {
			trq.BackfillTolerance = time.Second * time.Duration(i)
		}
	}
}
