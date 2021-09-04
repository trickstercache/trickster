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
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testDF4Response = `{
  "data": [
    [
      1,
      2,
      3
    ]
  ],
  "meta": [
    {
      "kind": "numeric",
      "label": "test",
      "tags": [
        "__check_uuid:11223344-5566-7788-9900-aabbccddeeff",
        "__name:test"
      ]
    }
  ],
  "version": "DF4",
  "head": {
    "count": 3,
    "start": 0,
    "period": 300
  }
}
`

const testDF4Response2 = `{
  "data": [
    [
      4,
      5,
      6
    ],
    [
      1,
	  2,
	  null
    ]
  ],
  "meta": [
    {
    "tags": [
      "__check_uuid:11223344-5566-7788-9900-aabbccddeeff",
      "__name:test"
    ],
    "label": "test",
    "kind": "numeric"
    },
    {
    "tags": [
      "__check_uuid:11223344-5566-7788-9900-aabbccddeeff",
      "__name:test1"
    ],
    "label": "test1",
    "kind": "numeric"
    }
  ],
  "version": "DF4",
  "head": {
    "count": 3,
    "start": 300,
    "period": 300
  }
}
`

func TestDF4SeriesEnvelopeSetTimeRangeQuery(t *testing.T) {
	se := DF4SeriesEnvelope{}
	const step = time.Duration(300) * time.Minute
	trq := &timeseries.TimeRangeQuery{Step: step}
	se.SetTimeRangeQuery(trq)
	if se.Step() != step {
		t.Errorf("Expected step: %v, got: %v", step, se.Step())
	}
}

func TestDF4SeriesEnvelopeSetExtents(t *testing.T) {
	se := &DF4SeriesEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{
		Start: time.Time{},
		End:   time.Time{},
	}}

	se.SetExtents(ex)
	e := se.Extents()
	if len(e) != 1 {
		t.Errorf("Expected length: 1, got: %d", len(e))
	}
}

func TestDF4SeriesEnvelopeSeriesCount(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testDF4Response), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*DF4SeriesEnvelope)
	if se.SeriesCount() != 1 {
		t.Errorf("Expected count: 1, got %d", se.SeriesCount())
	}
}

func TestDF4SeriesEnvelopeValueCount(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testDF4Response), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*DF4SeriesEnvelope)
	if se.ValueCount() != 3 {
		t.Errorf("Expected count: 3, got %d", se.ValueCount())
	}
}

func TestDF4SeriesEnvelopeTimestampCount(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testDF4Response2), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*DF4SeriesEnvelope)
	if se.TimestampCount() != 3 {
		t.Errorf("Expected count: 3, got %d", se.TimestampCount())
	}
}

func TestDF4SeriesEnvelopeMerge(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testDF4Response), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*DF4SeriesEnvelope)
	ts2, err := UnmarshalTimeseries([]byte(testDF4Response2), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se2 := ts2.(*DF4SeriesEnvelope)
	se1.Merge(true, se2)
	if se1.SeriesCount() != 2 {
		t.Errorf("Expected count: 2, got: %v", se1.SeriesCount())
	}

	if se1.ValueCount() != 8 {
		t.Errorf("Expected count: 8, got: %v", se1.ValueCount())
	}

	// disabled until Merge functionality can be made deterministic

	// if se1.Data[0][0] != 1.0 {
	// 	t.Errorf("Expected first value: 1, got: %v", se1.Data[0][0])
	// }

	// if se1.Data[0][3] != 6.0 {
	// 	t.Errorf("Expected last value: 6, got: %v", se1.Data[0][3])
	// }
}

func TestDF4SeriesEnvelopeClone(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testDF4Response), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts1.(*DF4SeriesEnvelope)
	se2 := se.Clone()

	s1, err := MarshalTimeseries(se, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	s2, err := MarshalTimeseries(se2, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	if string(s1) != string(s2) {
		t.Errorf("Expected %s = %s", string(s1), string(s2))
	}
}

func TestDF4SeriesEnvelopeCropToRange(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testDF4Response), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*DF4SeriesEnvelope)
	se1.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(600, 0),
	}})

	se1.CropToRange(timeseries.Extent{
		Start: time.Unix(100, 0),
		End:   time.Unix(500, 0),
	})

	b, err := MarshalTimeseries(se1, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp := `{"data":[[1]],"meta":[{"kind":"numeric","label":"test",` +
		`"tags":["__check_uuid:11223344-5566-7788-9900-aabbccddeeff",` +
		`"__name:test"]}],"version":"DF4","head":{"count":1,"start":0,` +
		`"period":300},"extents":[{"start":"` +
		time.Unix(0, 0).Format(time.RFC3339) +
		`","end":"` + time.Unix(300, 0).Format(time.RFC3339) + `"}]}`
	if string(b) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(b))
	}

	// Test crop outside extents.
	se1.CropToRange(timeseries.Extent{
		Start: time.Unix(900, 0),
		End:   time.Unix(1200, 0),
	})

	b, err = MarshalTimeseries(se1, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp = `{"data":[],"version":"DF4",` +
		`"head":{"count":0,"start":900,"period":300}}`
	if string(b) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(b))
	}
}

func TestDF4SeriesEnvelopeCropToSize(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testDF4Response), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*DF4SeriesEnvelope)
	se1.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(600, 0),
	}})

	se1.CropToSize(2, time.Unix(600, 0), timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(600, 0),
	})

	b, err := MarshalTimeseries(se1, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp := `{"data":[[2,3]],"meta":[{"kind":"numeric","label":"test",` +
		`"tags":["__check_uuid:11223344-5566-7788-9900-aabbccddeeff",` +
		`"__name:test"]}],"version":"DF4","head":{"count":2,` +
		`"start":300,"period":300},` +
		`"extents":[{"start":"` +
		time.Unix(300, 0).Format(time.RFC3339) + `",` +
		`"end":"` + time.Unix(600, 0).Format(time.RFC3339) + `"}]}`
	if string(b) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(b))
	}

	se1.ExtentList = timeseries.ExtentList{}
	se1.CropToSize(2, time.Unix(600, 0), timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(600, 0),
	})

	if len(se1.Data) > 0 {
		t.Errorf("Expected data length: 0, got: %v", len(se1.Data))
	}
}

func TestMarshalDF4Timeseries(t *testing.T) {
	se := &DF4SeriesEnvelope{
		Data: [][]interface{}{{1, 2, 3}},
		Meta: []map[string]interface{}{{
			"tags": []string{
				"__check_uuid:11223344-5566-7788-9900-aabbccddeeff",
				"__name:test",
			},
			"label": "test",
			"kind":  "numeric",
		}},
		Ver: "DF4",
		Head: DF4Info{
			Count:  3,
			Start:  0,
			Period: 300,
		},
	}

	bytes, err := MarshalTimeseries(se, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp := strings.Replace(strings.Replace(testDF4Response, "\n", "", -1),
		" ", "", -1)
	if string(bytes) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(bytes))
	}

}

func TestUnmarshalDF4Timeseries(t *testing.T) {
	bytes := []byte(testDF4Response2)
	ts, err := UnmarshalTimeseries(bytes, nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*DF4SeriesEnvelope)
	if len(se.Data) != 2 {
		t.Errorf(`expected length: 2. got %d`, len(se.Data))
		return
	}

	if se.Data[1][1] != 2.0 {
		t.Errorf(`expected value: 2.0. got %f`, se.Data[1][1])
		return
	}

	if se.Head.Start != 300 {
		t.Errorf(`expected time start: 300. got %d`, se.Head.Start)
		return
	}

	if se.Head.Period != 300 {
		t.Errorf(`expected time period: 300. got %d`, se.Head.Period)
		return
	}
}

func TestSize(t *testing.T) {

	s, _ := UnmarshalTimeseries([]byte(testDF4Response), nil)

	if s.Size() != 136 {
		t.Errorf("expected %d got %d", 136, s.Size())
	}

}
