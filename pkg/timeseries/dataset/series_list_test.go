package dataset

import (
	"testing"
)

func TestEqual(t *testing.T) {

	sl := SeriesList{testSeries()}
	if sl.Equal(nil) {
		t.Error("expected false")
	}

	s := testSeries()
	sl2 := SeriesList{s}

	sl2[0].Header.Name = "test2"

	if sl.Equal(sl2) {
		t.Error("expected false")
	}

}

func TestListMerge(t *testing.T) {

	sl := SeriesList{testSeries()}
	if sl.Equal(nil) {
		t.Error("expected false")
	}

	sl2 := SeriesList{testSeries(), testSeries()}
	sl2[1].Header.Name = "test2"

	sl = sl.merge(sl2)

	if len(sl) != 2 {
		t.Error("expected 2 got", len(sl))
	}

}
