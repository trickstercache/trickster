package influxdb

import (
	"testing"
	"net/http"
	"net/url"
	"fmt"
	"github.com/influxdata/influxdb/pkg/testing/assert"
)

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: "q=SELECT%20mean(%22value%22)%20FROM%20%22monthly%22.%22bandwidth.1min%22%20WHERE%20(%22cdn%22%20%3D%20%27over-the-top%27)%20AND%20time%20%3E%3D%20now()%20-%206h%20GROUP%20BY%20time(15s)%2C%20%22cachegroup%22%20fill(null)&epoch=ms",
	}}
	client := &Client{}
	ans, err := client.ParseTimeRangeQuery(req)
	if (err != nil) {
		fmt.Println(err.Error())
	} else {
		fmt.Println(ans.Extent.Start.UTC())
		fmt.Println(ans.Extent.End.UTC())
		fmt.Println(ans.Extent.End.UTC().Hour() - ans.Extent.Start.UTC().Hour())
		assert.Equal(t, ans.Extent.End.UTC().Hour() - ans.Extent.Start.UTC().Hour(), 6)
	}
}

//ToDo
//Do tests with both times with now
//Do tests for + with now
//Do tests for without now, both + and -