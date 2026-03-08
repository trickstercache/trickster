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

package timeseries

import (
	"net/url"
	"testing"
	"time"
)

func TestNormalizeExtent(t *testing.T) {
	tmrw := time.Now().Add(time.Duration(24) * time.Hour).Unix()
	expected := (time.Now().Unix() / 10) * 10

	tests := []struct {
		name                 string
		start, end, stepSecs int64
		isOffset             bool
		rangeStart, rangeEnd int64
	}{
		{
			name:  "basic no change",
			start: 1, end: 100, stepSecs: 1,
			rangeStart: 1, rangeEnd: 100,
		},
		{
			name:  "aligns to step",
			start: 1, end: 103, stepSecs: 10,
			rangeStart: 0, rangeEnd: 100,
		},
		{
			name:  "clamps future end to now",
			start: 1, end: tmrw, stepSecs: 10,
			rangeStart: 0, rangeEnd: expected,
		},
		{
			name:  "isOffset skips future clamp",
			start: 1, end: tmrw, stepSecs: 10, isOffset: true,
			rangeStart: 0, rangeEnd: (tmrw / 10) * 10,
		},
		{
			name:  "zero step no normalization",
			start: 1, end: 103, stepSecs: 0,
			rangeStart: 1, rangeEnd: 103,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq := TimeRangeQuery{
				Statement: "up",
				Extent: Extent{
					Start: time.Unix(test.start, 0),
					End:   time.Unix(test.end, 0),
				},
				Step:     time.Duration(test.stepSecs) * time.Second,
				IsOffset: test.isOffset,
			}

			trq.NormalizeExtent()

			if trq.Extent.Start.Unix() != test.rangeStart {
				t.Errorf("rangeStart: expected=%d actual=%d", test.rangeStart, trq.Extent.Start.Unix())
			}
			if trq.Extent.End.Unix() != test.rangeEnd {
				t.Errorf("rangeEnd: expected=%d actual=%d", test.rangeEnd, trq.Extent.End.Unix())
			}
		})
	}
}

func TestClone(t *testing.T) {
	t.Run("basic with TemplateURL", func(t *testing.T) {
		u, _ := url.Parse("http://127.0.0.1/")
		trq := &TimeRangeQuery{
			Statement:   "1234",
			Extent:      Extent{Start: time.Unix(5, 0), End: time.Unix(10, 0)},
			Step:        time.Duration(5) * time.Second,
			TemplateURL: u,
		}
		c := trq.Clone()
		if c.Statement != trq.Statement || c.Step != trq.Step {
			t.Error("basic fields mismatch")
		}
		if c.TemplateURL == trq.TemplateURL {
			t.Error("TemplateURL should be a different pointer")
		}
		if c.TemplateURL.String() != trq.TemplateURL.String() {
			t.Error("TemplateURL value mismatch")
		}
	})

	t.Run("nil TemplateURL", func(t *testing.T) {
		trq := &TimeRangeQuery{Statement: "test"}
		c := trq.Clone()
		if c.TemplateURL != nil {
			t.Error("expected nil TemplateURL")
		}
	})

	t.Run("OriginalBody independent copy", func(t *testing.T) {
		trq := &TimeRangeQuery{
			Statement:    "test",
			OriginalBody: []byte("original"),
		}
		c := trq.Clone()
		c.OriginalBody[0] = 'X'
		if trq.OriginalBody[0] == 'X' {
			t.Error("clone mutation affected original OriginalBody")
		}
	})

	t.Run("CacheKeyElements independent copy", func(t *testing.T) {
		trq := &TimeRangeQuery{
			Statement:        "test",
			CacheKeyElements: map[string]string{"key": "val"},
		}
		c := trq.Clone()
		c.CacheKeyElements["key"] = "mutated"
		if trq.CacheKeyElements["key"] == "mutated" {
			t.Error("clone mutation affected original CacheKeyElements")
		}
	})

	t.Run("TagFieldDefintions independent copy", func(t *testing.T) {
		trq := &TimeRangeQuery{
			Statement:          "test",
			TagFieldDefintions: FieldDefinitions{{Name: "host", DataType: String}},
		}
		c := trq.Clone()
		c.TagFieldDefintions[0].Name = "mutated"
		if trq.TagFieldDefintions[0].Name == "mutated" {
			t.Error("clone mutation affected original TagFieldDefintions")
		}
	})
}

func TestSizeTRQ(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1/")
	trq := &TimeRangeQuery{Statement: "1234", Extent: Extent{
		Start: time.Unix(5, 0),
		End:   time.Unix(10, 0),
	}, Step: time.Duration(5) * time.Second, TemplateURL: u}
	size := trq.Size()
	if size != 119 {
		t.Errorf("expected %d got %d", 119, size)
	}
}

func TestExtractBackfillTolerance(t *testing.T) {
	t.Run("valid flag", func(t *testing.T) {
		trq := &TimeRangeQuery{}
		trq.ExtractBackfillTolerance("testing trickster-backfill-tolerance:30 ")
		if trq.BackfillTolerance != time.Second*30 {
			t.Error("expected 30s got", trq.BackfillTolerance)
		}
	})

	t.Run("flag not present", func(t *testing.T) {
		trq := &TimeRangeQuery{}
		trq.ExtractBackfillTolerance("no flag here")
		if trq.BackfillTolerance != 0 {
			t.Error("expected 0 got", trq.BackfillTolerance)
		}
	})

	t.Run("flag at position 0", func(t *testing.T) {
		trq := &TimeRangeQuery{}
		trq.ExtractBackfillTolerance("trickster-backfill-tolerance:30")
		// x > 1 check means position 0 is not extracted
		if trq.BackfillTolerance != 0 {
			t.Error("expected 0 for position 0, got", trq.BackfillTolerance)
		}
	})
}

func TestStringTRQ(t *testing.T) {
	const expected = `{"statement":"1234","step":"5s","extent":"5000-10000","tsd":{"name":"","type":0},"td":[],"vd":null}`
	trq := &TimeRangeQuery{Statement: "1234", Extent: Extent{
		Start: time.Unix(5, 0),
		End:   time.Unix(10, 0),
	}, Step: time.Duration(5) * time.Second}
	s := trq.String()

	if s != expected {
		t.Errorf("%s\n%s", s, expected)
	}
}

func TestGetBackfillTolerance(t *testing.T) {
	tests := []struct {
		name      string
		tolerance time.Duration
		step      time.Duration
		def       time.Duration
		points    int
		expected  time.Duration
	}{
		{
			name:     "returns default when tolerance is 0",
			def:      time.Second * 5,
			expected: time.Second * 5,
		},
		{
			name:      "returns override when positive",
			tolerance: time.Second * 30,
			def:       time.Second * 5,
			expected:  time.Second * 30,
		},
		{
			name:     "points override when larger than default",
			step:     5 * time.Second,
			def:      time.Second * 5,
			points:   10,
			expected: time.Second * 50,
		},
		{
			name:      "returns 0 when negative",
			tolerance: -1,
			step:      5 * time.Second,
			def:       time.Second * 5,
			points:    10,
			expected:  0,
		},
		{
			name:     "points not larger than default uses default",
			step:     1 * time.Second,
			def:      time.Second * 50,
			points:   3,
			expected: time.Second * 50,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trq := &TimeRangeQuery{
				BackfillTolerance: test.tolerance,
				Step:              test.step,
			}
			if got := trq.GetBackfillTolerance(test.def, test.points); got != test.expected {
				t.Errorf("expected %s got %s", test.expected, got)
			}
		})
	}
}
