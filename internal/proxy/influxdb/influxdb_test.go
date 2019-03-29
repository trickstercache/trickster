package influxdb

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/influxdata/influxdb/pkg/testing/assert"
)

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		//SELECT mean("value") FROM "monthly"."bandwidth.1min" WHERE ("cdn" = 'over-the-top') AND time >= now() - 6h GROUP BY time(15s), "cachegroup" fill(null)&epoch=ms
		RawQuery: "q=SELECT%20mean(%22value%22)%20FROM%20%22monthly%22.%22bandwidth.1min%22%20WHERE%20(%22cdn%22%20%3D%20%27over-the-top%27)%20AND%20time%20%3E%3D%20now()%20-%206h%20GROUP%20BY%20time(15s)%2C%20%22cachegroup%22%20fill(null)&epoch=ms",
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		fmt.Println(err.Error())
	} else {
		assert.Equal(t, int(res.Step), 15)
		assert.Equal(t, res.Extent.End.UTC().Hour()-res.Extent.Start.UTC().Hour(), 6)
	}
}

func TestParseTimeRangeQueryWithBothTimes(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		//SELECT mean("value") FROM "monthly"."bandwidth.1min" WHERE ("cdn" = 'over-the-top') AND time >= now() - 6h AND time < now() - 3h GROUP BY time(15s), "cachegroup" fill(null)&epoch=ms
		RawQuery: "q=SELECT%20mean(%22value%22)%20FROM%20%22monthly%22.%22bandwidth.1min%22%20WHERE%20(%22cdn%22%20%3D%20%27over-the-top%27)%20AND%20time%20%3E%3D%20now()%20-%206h%20AND%20time%20%3C%20now()%20-%203h%20GROUP%20BY%20time(15s)%2C%20%22cachegroup%22%20fill(null)&epoch=ms",
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		fmt.Println(err.Error())
	} else {
		assert.Equal(t, int(res.Step), 15)
		assert.Equal(t, res.Extent.End.UTC().Hour()-res.Extent.Start.UTC().Hour(), 3)
	}
}

func TestParseTimeRangeQueryWithoutNow(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		//SELECT mean("value") FROM "monthly"."bandwidth.1min" WHERE ("cdn" = 'over-the-top') AND time >= now() - 6h AND time > 2052926911485ms AND time < 52926911486ms GROUP BY time(15s), "cachegroup" fill(null)
		RawQuery: "q=SELECT%20mean(%22value%22)%20FROM%20%22monthly%22.%22bandwidth.1min%22%20WHERE%20(%22cdn%22%20%3D%20%27over-the-top%27)%20AND%20time%20%3E%2052926911485ms%20AND%20time%20%3C%2052926911486ms%20GROUP%20BY%20time(15s)%2C%20%22cachegroup%22%20fill(null)",
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		fmt.Println(err.Error())
	} else {
		assert.Equal(t, int(res.Step), 15)
		assert.Equal(t, res.Extent.End.UTC().Second()-res.Extent.Start.UTC().Second(), 1)
	}
}

func TestParseTimeRangeQueryWithAbsoluteTime(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		//SELECT mean("value") FROM "monthly"."bandwidth.1min" WHERE ("cdn" = 'over-the-top') AND time < 2052926911486ms GROUP BY time(15s), "cachegroup" fill(null)
		RawQuery: "q=SELECT%20mean(%22value%22)%20FROM%20%22monthly%22.%22bandwidth.1min%22%20WHERE%20(%22cdn%22%20%3D%20%27over-the-top%27)%20AND%20time%20%3C%2052926911486ms%20GROUP%20BY%20time(15s)%2C%20%22cachegroup%22%20fill(null)",
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(req)
	if err != nil {
		fmt.Println(err.Error())
	} else {
		assert.Equal(t, int(res.Step), 15)
		assert.Equal(t, res.Extent.Start.UTC().IsZero(), true)
	}
}
