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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const testResponse = `[
	[600.000,1.75],
	[0,1],
	[300.000,1.5]
]
`
const testResponse2 = `[
	[300.000,2],
	[900.000,2.75],
	[600.000,2.5],
	[1200.000,3]
]`

func TestDataPointMarshalJSON(t *testing.T) {
	dp := &DataPoint{
		Time:  time.Unix(99900, 0),
		Value: 1.5,
	}

	b, err := dp.MarshalJSON()
	if err != nil {
		t.Error(err)
		t.FailNow()
	}

	exp := `[99900,1.5]`
	if string(b) != exp {
		t.Errorf("Expected JSON: %v, got: %v", exp, string(b))
	}

	ts := `[
		1556290800,
		300,
		{
		  "+23e-004": 1,
		  "+85e-004": 1
		}
	]`

	dp.UnmarshalJSON([]byte(ts))
	if dp.Step != 300 {
		t.Errorf("Expected step: 300, got: %v", dp.Step)
	}

	mv, ok := dp.Value.(map[string]interface{})
	if !ok {
		t.Errorf("Unexpected histogram value type: %v", dp.Value)
		t.FailNow()
	}

	if mv["+85e-004"] != float64(1) {
		t.Errorf("Expected histogram value: 1, got: %v", mv["+85e-004"])
	}

	ts = `[
		1556290800,
		23x,
		{
		  "+23e-004": 1,
		  "+85e-004": 1
		}
	]`

	dp = &DataPoint{}
	err = dp.UnmarshalJSON([]byte(ts))
	if err == nil {
		t.Error("expected error for invalid character")
	}

}

func TestSeriesEnvelopeSetTimeRangeQuery(t *testing.T) {
	se := SeriesEnvelope{}
	const step = time.Duration(300) * time.Minute
	trq := &timeseries.TimeRangeQuery{Step: step}
	se.SetTimeRangeQuery(trq)
	if se.Step() != step {
		t.Errorf("Expected step: %v, got: %v", step, se.StepDuration)
	}
}

func TestSeriesEnvelopeSetExtents(t *testing.T) {
	se := &SeriesEnvelope{}
	ex := timeseries.ExtentList{timeseries.Extent{
		Start: time.Time{},
		End:   time.Time{},
	}}

	se.SetExtents(ex)
	if len(se.ExtentList) != 1 {
		t.Errorf("Expected length: 1, got: %d", len(se.ExtentList))
	}
}

func TestSeriesEnvelopeExtents(t *testing.T) {
	se := &SeriesEnvelope{}
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

func TestSeriesEnvelopeSeriesCount(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)
	if se.SeriesCount() != 1 {
		t.Errorf("Expected count: 1, got %d", se.SeriesCount())
	}
}

func TestSeriesEnvelopeValueCount(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)
	if se.ValueCount() != 3 {
		t.Errorf("Expected count: 3, got %d", se.ValueCount())
	}
}

func TestSeriesEnvelopeTimestampCount(t *testing.T) {
	ts, err := UnmarshalTimeseries([]byte(testResponse2), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)
	if se.TimestampCount() != 4 {
		t.Errorf("Expected count: 4, got %d", se.TimestampCount())
	}
}

func TestSeriesEnvelopeMerge(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*SeriesEnvelope)
	ts2, err := UnmarshalTimeseries([]byte(testResponse2), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se2 := ts2.(*SeriesEnvelope)
	se1.Merge(true, se2)
	if se1.ValueCount() != 7 {
		t.Errorf("Expected count: 7, got: %v", se1.ValueCount())
	}

	if se1.Data[0].Value != 1.0 {
		t.Errorf("Expected first value: 1, got: %v", se1.Data[0].Value)
	}

	if se1.Data[6].Value != 3.0 {
		t.Errorf("Expected last value: 3, got: %v", se1.Data[6].Value)
	}
}

func TestSeriesEnvelopeSort(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts1.(*SeriesEnvelope)
	se.Sort()
	if len(se.Data) != 3 {
		t.Errorf("Expected length: 3, got: %v", len(se.Data))
	}

	if se.Data[0].Value != 1.0 {
		t.Errorf("Expected first value: 1, got: %v", se.Data[0].Value)
	}

	if se.Data[2].Value != 1.75 {
		t.Errorf("Expected last value: 1.75, got: %v", se.Data[2].Value)
	}
}

func TestSeriesEnvelopeClone(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts1.(*SeriesEnvelope)
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

func TestSeriesEnvelopeCropToRange(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*SeriesEnvelope)
	se1.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(600, 0),
	}})

	ts2, err := UnmarshalTimeseries([]byte(testResponse2), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se2 := ts2.(*SeriesEnvelope)
	se2.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(300, 0),
		End:   time.Unix(1200, 0),
	}})

	se1.Merge(true, se2)
	se1.CropToRange(timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(300, 0),
	})

	s1, err := MarshalTimeseries(se1, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp := `{"data":[[0,1],[300,1.5],[300,2]],` +
		`"extents":[{"start":"` + time.Unix(0, 0).Format(time.RFC3339) + `",` +
		`"end":"` + time.Unix(300, 0).Format(time.RFC3339) + `"}]}`
	if string(s1) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(s1))
	}
}

func TestSeriesEnvelopeCropToSize(t *testing.T) {
	ts1, err := UnmarshalTimeseries([]byte(testResponse), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*SeriesEnvelope)
	se1.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(300, 0),
	}})

	ts2, err := UnmarshalTimeseries([]byte(testResponse2), nil)
	if err != nil {
		t.Error(err)
		return
	}

	se2 := ts2.(*SeriesEnvelope)
	se2.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(300, 0),
		End:   time.Unix(1200, 0),
	}})

	se1.Merge(true, se2)
	se1.CropToSize(2, time.Unix(900, 0), timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(900, 0),
	})

	s1, err := MarshalTimeseries(se1, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp := `{"data":[[600,1.75],[600,2.5],[900,2.75]],` +
		`"extents":[{"start":"` + time.Unix(600, 0).Format(time.RFC3339) +
		`","end":"` + time.Unix(900, 0).Format(time.RFC3339) + `"}]}`
	if string(s1) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(s1))
	}

	se1.ExtentList = timeseries.ExtentList{}
	se1.CropToSize(2, time.Unix(900, 0), timeseries.Extent{
		Start: time.Unix(0, 0),
		End:   time.Unix(900, 0),
	})

	if len(se1.Data) > 0 {
		t.Errorf("Expected data length: 0, got: %v", len(se1.Data))
	}
}

func TestMarshalTimeseries(t *testing.T) {
	se := &SeriesEnvelope{
		Data: DataPoints{
			DataPoint{
				Time:  time.Unix(99000, 0),
				Value: 1.5,
			},
			DataPoint{
				Time:  time.Unix(99000, 500000000),
				Value: 1.5,
			},
		},
	}

	bytes, err := MarshalTimeseries(se, nil, 200)
	if err != nil {
		t.Error(err)
		return
	}

	exp := `[[99000,1.5],[99000.5,1.5]]`
	if string(bytes) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(bytes))
	}

}

func TestUnmarshalTimeseries(t *testing.T) {
	bytes := []byte(`[[99000,1.5],[99000.500,1.5]]`)
	ts, err := UnmarshalTimeseries(bytes, nil)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)
	if len(se.Data) != 2 {
		t.Errorf(`Expected length: 2. got %d`, len(se.Data))
		return
	}

	if se.Data[1].Value != 1.5 {
		t.Errorf(`Expected value: 1.5. got %f`, se.Data[1].Value)
		return
	}

	if se.Data[1].Time.Unix() != 99000 {
		t.Errorf(`Expected time secs: 99000. got %d`, se.Data[1].Time.Unix())
		return
	}

	if se.Data[1].Time.Nanosecond()/1000000 != 500 {
		t.Errorf(`Expected time nano: 500. got %d`,
			se.Data[1].Time.Nanosecond()/1000000)
		return
	}

	bytes = []byte(`{"data":[[99000,1.5],[99000.500,1.5]],"step":"300s"}`)
	ts, err = UnmarshalTimeseries(bytes, nil)
	if err != nil {
		t.Error(err)
		return
	}

	se = ts.(*SeriesEnvelope)
	if len(se.Data) != 2 {
		t.Errorf(`Expected length: 2. got %d`, len(se.Data))
		return
	}

	if se.Data[1].Value != 1.5 {
		t.Errorf(`Expected value: 1.5. got %f`, se.Data[1].Value)
		return
	}

	if se.Data[1].Time.Unix() != 99000 {
		t.Errorf(`Expected time secs: 99000. got %d`, se.Data[1].Time.Unix())
		return
	}

	if se.Data[1].Time.Nanosecond()/1000000 != 500 {
		t.Errorf(`Expected time nano: 500. got %d`,
			se.Data[1].Time.Nanosecond()/1000000)
		return
	}

	if se.Step() != 300*time.Second {
		t.Errorf("Expected step: 300s, got: %v", se.Step())
		return
	}
}

func TestUnmarshalInstantaneous(t *testing.T) {
	bytes := []byte(`[[99000,1.5],[99000.500,1.5]]`)
	ts, err := UnmarshalInstantaneous(bytes)
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)
	if len(se.Data) != 2 {
		t.Errorf(`Expected length: 2. got %d`, len(se.Data))
		return
	}

	if se.Data[1].Value != 1.5 {
		t.Errorf(`Expected value: 1.5. got %f`, se.Data[1].Value)
		return
	}

	if se.Data[1].Time.Unix() != 99000 {
		t.Errorf(`Expected time secs: 99000. got %d`, se.Data[1].Time.Unix())
		return
	}

	if se.Data[1].Time.Nanosecond()/1000000 != 500 {
		t.Errorf(`Expected time nano: 500. got %d`,
			se.Data[1].Time.Nanosecond()/1000000)
		return
	}
}

func TestTSSize(t *testing.T) {

	bytes := []byte(`[[99000,1.5],[99000.500,1.5]]`)

	s, _ := UnmarshalTimeseries(bytes, nil)

	size := s.Size()

	if size != 96 {
		t.Errorf("expected %d got %d", 96, size)
	}

}

func TestMarshalJSONEnvelope(t *testing.T) {

	se := SeriesEnvelope{StepDuration: time.Duration(1) * time.Hour}
	_, err := se.MarshalJSON()
	if err != nil {
		t.Error(err)
	}

}

func TestUnMarshalJSONEnvelope(t *testing.T) {

	bytes := []byte(`"data"."extents"."step"`)
	se := &SeriesEnvelope{}
	err := se.UnmarshalJSON(bytes)
	if err == nil {
		t.Error("expected error for invalid character")
	}

}
