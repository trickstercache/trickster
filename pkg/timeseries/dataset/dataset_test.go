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

package dataset

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

func testDataSet() *DataSet {
	ds := &DataSet{
		Results:            []*Result{testResult()},
		ExtentList:         timeseries.ExtentList{timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(10, 0)}},
		TimeRangeQuery:     &timeseries.TimeRangeQuery{Step: time.Duration(5 * timeseries.Second)},
		VolatileExtentList: timeseries.ExtentList{timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(10, 0)}},
	}
	ds.Merger = ds.DefaultMerger
	ds.SizeCropper = ds.DefaultSizeCropper
	ds.RangeCropper = ds.DefaultRangeCropper
	ds.Sorter = func() {}
	return ds
}

func genTestDataSet(seriesCount int, resultsCount int) *DataSet {
	if resultsCount <= 0 {
		logger.Error("resultsCount must be greater than 0", nil)
		resultsCount = 1
	}
	results := make([]*Result, 0, resultsCount)
	for i := range resultsCount {
		seriesList := make([]*Series, seriesCount)
		for j := range seriesCount {
			sh := testSeriesHeader()
			sh.Name = "test" + string(fmt.Sprintf("%d-%d", i, j))
			sh.CalculateHash()
			points := newPoints()
			seriesList[j] = &Series{
				Header:    sh,
				Points:    points,
				PointSize: points.Size(),
			}
		}
		r := &Result{
			StatementID: i,
			SeriesList:  seriesList,
		}
		results = append(results, r)
	}

	ds := &DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{Step: time.Duration(5 * timeseries.Second)},
		Results:        results,
		ExtentList:     timeseries.ExtentList{timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(30, 0)}},
	}

	ds.Merger = ds.DefaultMerger
	ds.SizeCropper = ds.DefaultSizeCropper
	ds.RangeCropper = ds.DefaultRangeCropper
	ds.Sorter = func() {}
	return ds
}

func newPoints() Points {
	return generateNewPoints(6, 5, 0, func(i int) []any {
		return []any{1}
	})
}

func generateNewPoints(points, interval, offset int, valueFn func(i int) []any) Points {
	result := make(Points, points)
	for i := range points {
		epochTime := epoch.Epoch((offset + interval + i*interval) * timeseries.Second)
		values := valueFn(i)
		size := 16 * len(values)
		result[i] = Point{
			Epoch:  epochTime,
			Size:   size,
			Values: values,
		}
	}
	return result
}

func testDataSet2() *DataSet {

	sh1 := testSeriesHeader()
	sh1.Name = "test1"
	sh1.CalculateHash()

	sh2 := testSeriesHeader()
	sh2.Name = "test2"
	sh2.CalculateHash()

	sh3 := testSeriesHeader()
	sh3.Name = "test3"
	sh3.CalculateHash()

	sh4 := testSeriesHeader()
	sh4.Name = "test4"
	sh4.CalculateHash()
	s := newPoints().Size()
	// r1 s1
	r1 := &Result{
		StatementID: 0,
		SeriesList: []*Series{
			{sync.Mutex{}, sh1, newPoints(), s},
			nil,
		},
	}

	r2 := &Result{
		StatementID: 1,
		SeriesList: []*Series{
			{sync.Mutex{}, sh2, newPoints(), s},
			{sync.Mutex{}, sh3, newPoints(), s},
			{sync.Mutex{}, sh4, newPoints(), s},
		},
	}

	ds := &DataSet{
		TimeRangeQuery: &timeseries.TimeRangeQuery{Step: time.Duration(5 * timeseries.Second)},
		Results:        []*Result{r1, r2, nil},
		ExtentList:     timeseries.ExtentList{timeseries.Extent{Start: time.Unix(5, 0), End: time.Unix(30, 0)}},
	}

	ds.Merger = ds.DefaultMerger
	ds.SizeCropper = ds.DefaultSizeCropper
	ds.RangeCropper = ds.DefaultRangeCropper
	ds.Sorter = func() {}
	return ds
}

func TestDataSetClone(t *testing.T) {
	ds := testDataSet()
	ts := ds.Clone()
	ds2 := ts.(*DataSet)
	if len(ds2.ExtentList) != len(ds.ExtentList) {
		t.Error("dataset clone mismatch")
	}
}

func TestSort(t *testing.T) {
	var x int
	testFunc := func() {
		x = 20
	}
	ds := &DataSet{Sorter: testFunc}
	ds.Sort()
	if x != 20 {
		t.Error("sortfunc error")
	}
}

func TestSetExtents(t *testing.T) {
	ds := &DataSet{}
	ex := timeseries.ExtentList{timeseries.Extent{Start: time.Time{}, End: time.Time{}}}
	ds.SetExtents(ex)
	if len(ds.Extents()) != 1 {
		t.Errorf(`expected 1. got %d`, len(ds.ExtentList))
	}
}

func TestTimestampCount(t *testing.T) {
	ds := testDataSet()
	if ds.TimestampCount() != 2 {
		t.Errorf("expected 2 got %d", ds.TimestampCount())
	}
}

func TestStepDuration(t *testing.T) {
	ds := testDataSet()
	if int(ds.Step().Seconds()) != 5 {
		t.Errorf("expected 5 got %d", int(ds.Step().Seconds()))
	}
}

func TestSetTimeRangeQuery(t *testing.T) {
	ds := testDataSet()
	trq := &timeseries.TimeRangeQuery{Step: time.Duration(1 * timeseries.Second)}

	ds.SetTimeRangeQuery(trq)
	if int(ds.Step().Seconds()) != 1 {
		t.Errorf("expected 1 got %d", int(ds.Step().Seconds()))
	}
}

func TestValueCount(t *testing.T) {
	ds := testDataSet()
	if ds.ValueCount() != 2 {
		t.Errorf("expected 2 got %d", ds.ValueCount())
	}
	ds.Results[0] = &Result{}
	if ds.ValueCount() != 0 {
		t.Errorf("expected 0 got %d", ds.ValueCount())
	}
}

func TestSeriesCount(t *testing.T) {
	ds := testDataSet()
	if ds.SeriesCount() != 1 {
		t.Errorf("expected 1 got %d", ds.ValueCount())
	}
	ds.Results[0] = &Result{}
	if ds.SeriesCount() != 0 {
		t.Errorf("expected 0 got %d", ds.ValueCount())
	}
}

func TestMerge(t *testing.T) {
	ds := &DataSet{}
	ds.Merge(false, nil)
	if len(ds.Results) > 0 {
		t.Error("dataset merge error")
	}

	ds = testDataSet2()
	ds2 := testDataSet2()
	ds.Results = ds.Results[:1]

	ds.Merge(false, ds2)

	if ds.SeriesCount() != 4 {
		t.Errorf("expected %d got %d", 4, ds.SeriesCount())
	}
}

func TestSize(t *testing.T) {

	ds := testDataSet()
	s := ds.Size()
	const expected = 237

	if s != expected {
		t.Errorf("expected %d got %d", expected, s)
	}
}

func TestVolatileExtents(t *testing.T) {

	ds := testDataSet()
	expected := 1
	e := ds.VolatileExtents().Clone()
	l := len(e)
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}

	e = append(e, timeseries.Extent{})
	ds.SetVolatileExtents(e)

	e = ds.VolatileExtents().Clone()
	l = len(e)
	expected = 2
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestMarshalDataSet(t *testing.T) {

	_, err := MarshalDataSet(nil, &timeseries.RequestOptions{}, 200)
	if err != timeseries.ErrUnknownFormat {
		t.Errorf("expected unknown format error, got %s", err.Error())
	}

	ds := testDataSet()
	trq := ds.TimeRangeQuery
	b, err := MarshalDataSet(ds, &timeseries.RequestOptions{}, 200)
	if err != nil {
		t.Error(err)
	}
	if len(b) == 0 {
		t.Error("invalid length")
	}
	ts, err := UnmarshalDataSet(b, trq)
	if err != nil {
		t.Error(err)
	}
	var ok bool
	if _, ok = ts.(*DataSet); !ok {
		t.Error("invalid dataset")
	}
}

func TestCroppedClone(t *testing.T) {

	// an extent fully inside of time series's extent
	ex := timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(20, 0)}

	// an extent fully surrounding the time series's extent
	ex2 := timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(35, 0)}

	// an extent having no intersection with the time series's extent
	// an extent fully outside of time series's extent
	ex3 := timeseries.Extent{Start: time.Unix(35, 0), End: time.Unix(50, 0)}

	ds := testDataSet2().CroppedClone(ex)
	exs := ds.Extents()
	if len(exs) != 1 || (exs[0].Start != time.Unix(15, 0) || exs[0].End != time.Unix(20, 0)) {
		t.Error("invalid extent in clone")
	}

	ts := testDataSet2()
	ts.ExtentList = nil
	ds = ts.CroppedClone(ex)
	ts, _ = ds.(*DataSet)
	if ts.ExtentList != nil {
		t.Error("invalid extent in clone")
	}

	ts = testDataSet2()
	ds = ts.CroppedClone(ex2)
	exs = ds.Extents()
	if len(exs) != 1 || (exs[0].Start != time.Unix(5, 0) || exs[0].End != time.Unix(30, 0)) {
		t.Error("invalid extent in clone", exs)
	}

	ts = testDataSet2()
	ds = ts.CroppedClone(ex3)
	exs = ds.Extents()
	if len(exs) != 0 {
		t.Error("invalid extent in clone", exs)
	}
}

func TestCropToRange(t *testing.T) {

	// an extent fully inside of time series's extent
	ex := timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(20, 0)}

	// an extent fully surrounding the time series's extent
	ex2 := timeseries.Extent{Start: time.Unix(0, 0), End: time.Unix(35, 0)}

	// an extent having no intersection with the time series's extent
	// an extent fully outside of time series's extent
	ex3 := timeseries.Extent{Start: time.Unix(35, 0), End: time.Unix(50, 0)}

	ds := testDataSet2()

	ds.RangeCropper = nil
	ds.CropToRange(ex)
	exs := ds.Extents()
	if len(exs) != 1 || (exs[0].Start != time.Unix(15, 0) || exs[0].End != time.Unix(20, 0)) {
		t.Error("invalid extent in crop")
	}

	ds = testDataSet2()
	ds.RangeCropper = ds.DefaultRangeCropper
	ds.CropToRange(ex2)
	exs = ds.ExtentList
	if len(exs) != 1 || (exs[0].Start != time.Unix(5, 0) || exs[0].End != time.Unix(30, 0)) {
		t.Error("invalid extent in crop", exs)
	}

	ds = testDataSet2()
	ds.DefaultRangeCropper(ex3)
	exs = ds.ExtentList
	if len(exs) != 0 {
		t.Error("invalid extent in crop", exs)
	}

	ds = testDataSet2()
	ds.ExtentList = timeseries.ExtentList{}
	ds.DefaultRangeCropper(ex3)
	exs = ds.ExtentList
	if len(exs) != 0 {
		t.Error("invalid extent in crop", exs)
	}

	t.Run("remove empty or nil series", func(t *testing.T) {
		// Create a fresh dataset for this test
		ds := genTestDataSet(2, 2)

		// Verify initial state
		originalSeriesCount := len(ds.Results[0].SeriesList)
		if originalSeriesCount != 2 {
			t.Fatalf("unexpected initial series count: got %d, expected 2", originalSeriesCount)
		}

		// First, crop with all series intact - should maintain the same count
		ex := timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(125, 0)}
		ds.DefaultRangeCropper(ex)

		// After first crop, we should still have the same number of series
		afterFirstCropCount := len(ds.Results[0].SeriesList)
		if afterFirstCropCount != originalSeriesCount {
			t.Fatalf("after first crop: got %d series, expected %d", afterFirstCropCount, originalSeriesCount)
		}

		// Now explicitly set the points to nil for the first series
		ds = genTestDataSet(2, 2)
		ds.Results[0].SeriesList[0].Points = nil

		// Crop again - this should remove the series with nil points
		ex = timeseries.Extent{Start: time.Unix(15, 0), End: time.Unix(125, 0)}
		ds.DefaultRangeCropper(ex)

		// After second crop, we should have one less series
		afterSecondCropCount := len(ds.Results[0].SeriesList)
		expectedAfterSecondCrop := originalSeriesCount - 1

		if afterSecondCropCount != expectedAfterSecondCrop {
			t.Fatalf("after second crop: got %d series, expected %d",
				afterSecondCropCount, expectedAfterSecondCrop)
		}

		// Verify the remaining series is not nil
		if len(ds.Results[0].SeriesList) > 0 {
			if ds.Results[0].SeriesList[0] == nil {
				t.Error("expected non-nil series after crop")
			}
		} else {
			t.Error("expected at least one series to remain after crop")
		}
	})

}

func genBenchmarkPoint(e epoch.Epoch, valuect int) Point {
	out := Point{
		Epoch:  e,
		Values: make([]any, valuect),
	}
	for i := range valuect {
		out.Values[i] = rand.Int() % 1000
	}
	return out
}

func genBenchmarkDataset(pointct int) *DataSet {
	if pointct > 1000 {
		pointct = 1000
	}
	bmSeries := &Series{
		Points: make(Points, pointct),
	}
	for i := 0; i < pointct; i++ {
		back := pointct - i
		t := epoch.Epoch(time.Now().Unix() - int64(back))
		bmSeries.Points[i] = genBenchmarkPoint(t, 4)
	}
	res := &Result{
		StatementID: 0,
		SeriesList:  []*Series{bmSeries},
	}
	return &DataSet{
		Results: []*Result{res},
	}
}

func BenchmarkMerge(b *testing.B) {
	dss := make([]*DataSet, b.N*2)
	for i := 0; i < b.N; i++ {
		dss[i] = genBenchmarkDataset(10)
		dss[i+1] = genBenchmarkDataset(10)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i += 2 {
		dss[i].Merge(true, dss[i+1])
	}
}

func BenchmarkCropToRange(b *testing.B) {
	dss := make([]*DataSet, b.N)
	for i := 0; i < b.N; i++ {
		dss[i] = genBenchmarkDataset(10)
	}
	bmExt := timeseries.Extent{
		Start: time.Now().Add(time.Second * -750),
		End:   time.Now().Add(time.Second * -250),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dss[i].CropToRange(bmExt)
	}
}
