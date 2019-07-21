package irondb

import (
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/timeseries"
)

const testResponse = `[
	[1435781460.750,1.75],
	[1435781430.000,1],
	[1435781445.500,1.5]
]
`
const testResponse2 = `[
	[1435781431.000,2],
	[1435781461.750,2.75],
	[1435781446.500,2.5]
]
`

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
}

func TestSeriesEnvelopeSetStep(t *testing.T) {
	se := SeriesEnvelope{}
	const step = time.Duration(300) * time.Minute
	se.SetStep(step)
	if se.StepDuration != step {
		t.Errorf("Expected step: %v, got: %v", step, se.StepDuration)
	}
}

func TestSeriesEnvelopeStep(t *testing.T) {
	se := SeriesEnvelope{}
	const step = time.Duration(300) * time.Minute
	se.SetStep(step)
	if se.Step() != step {
		t.Errorf("Expected step: %v, got: %v", step, se.Step())
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
	client := &Client{}
	ts, err := client.UnmarshalTimeseries([]byte(testResponse))
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
	client := &Client{}
	ts, err := client.UnmarshalTimeseries([]byte(testResponse))
	if err != nil {
		t.Error(err)
		return
	}

	se := ts.(*SeriesEnvelope)
	if se.ValueCount() != 3 {
		t.Errorf("Expected count: 3, got %d", se.ValueCount())
	}
}

func TestSeriesEnvelopeMerge(t *testing.T) {
	client := &Client{}
	ts1, err := client.UnmarshalTimeseries([]byte(testResponse))
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*SeriesEnvelope)
	ts2, err := client.UnmarshalTimeseries([]byte(testResponse2))
	if err != nil {
		t.Error(err)
		return
	}

	se2 := ts2.(*SeriesEnvelope)
	se1.Merge(true, se2)
	if se1.ValueCount() != 6 {
		t.Errorf("Expected count: 6, got: %v", se1.ValueCount())
	}

	if se1.Data[0].Value != 1.0 {
		t.Errorf("Expected first value: 1, got: %v", se1.Data[0].Value)
	}

	if se1.Data[5].Value != 2.75 {
		t.Errorf("Expected last value: 2.75, got: %v", se1.Data[5].Value)
	}
}

func TestSeriesEnvelopeSort(t *testing.T) {
	client := &Client{}
	ts1, err := client.UnmarshalTimeseries([]byte(testResponse))
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

func TestSeriesEnvelopeCopy(t *testing.T) {
	client := &Client{}
	ts1, err := client.UnmarshalTimeseries([]byte(testResponse))
	if err != nil {
		t.Error(err)
		return
	}

	se := ts1.(*SeriesEnvelope)
	se2 := se.Copy()

	s1, err := client.MarshalTimeseries(se)
	if err != nil {
		t.Error(err)
		return
	}

	s2, err := client.MarshalTimeseries(se2)
	if err != nil {
		t.Error(err)
		return
	}

	if string(s1) != string(s2) {
		t.Errorf("Expected %s = %s", string(s1), string(s2))
	}
}

func TestSeriesEnvelopeCrop(t *testing.T) {
	client := &Client{}
	ts1, err := client.UnmarshalTimeseries([]byte(testResponse))
	if err != nil {
		t.Error(err)
		return
	}

	se1 := ts1.(*SeriesEnvelope)
	se1.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(1435781430, 0),
		End:   time.Unix(1435781460, 750000000),
	}})

	ts2, err := client.UnmarshalTimeseries([]byte(testResponse2))
	if err != nil {
		t.Error(err)
		return
	}

	se2 := ts2.(*SeriesEnvelope)
	se2.SetExtents(timeseries.ExtentList{timeseries.Extent{
		Start: time.Unix(1435781430, 0),
		End:   time.Unix(1435781460, 750000000),
	}})

	se1.Merge(true, se2)
	se1.Crop(timeseries.Extent{
		Start: time.Unix(1435781430, 0),
		End:   time.Unix(1435781440, 0),
	})

	s1, err := client.MarshalTimeseries(se1)
	if err != nil {
		t.Error(err)
		return
	}

	exp := `{"data":[[1435781430,1],[1435781431,2]],` +
		`"extents":[{"start":"2015-07-01T16:10:30-04:00",` +
		`"end":"2015-07-01T16:10:40-04:00"}]}`
	if string(s1) != exp {
		t.Errorf("Expected JSON: %s, got: %s", exp, string(s1))
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

	client := &Client{}
	bytes, err := client.MarshalTimeseries(se)
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
	client := &Client{}
	ts, err := client.UnmarshalTimeseries(bytes)
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
	ts, err = client.UnmarshalTimeseries(bytes)
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
	client := &Client{}
	ts, err := client.UnmarshalInstantaneous(bytes)
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
