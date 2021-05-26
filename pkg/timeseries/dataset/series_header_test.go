package dataset

import (
	"testing"

	"github.com/trickstercache/trickster/pkg/timeseries"
)

func testHeader() *SeriesHeader {
	return &SeriesHeader{
		Name: "test",
		Tags: Tags{"tag1": "value1", "tag2": "trickster"},
		FieldsList: []timeseries.FieldDefinition{
			{
				Name:     "time",
				DataType: timeseries.Int64,
			},
			{
				Name:     "value1",
				DataType: timeseries.Int64,
			},
		},
		QueryStatement: "SELECT TRICKSTER!",
	}
}

func TestCalculateSeriesHeaderSize(t *testing.T) {

	const expected = 492
	sh := testHeader()
	i := sh.CalculateSize()
	if i != expected {
		t.Errorf("expected %d got %d", expected, i)
	}
}

func TestSeriesHeaderString(t *testing.T) {

	const expected = `{"name":"test","query":"SELECT TRICKSTER!","tags":"tag1=value1;tag2=trickster","fields":["time","value1"],"timestampIndex":0}`

	if s := testHeader().String(); s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}
