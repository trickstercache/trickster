/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package influxdb

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/Comcast/trickster/internal/proxy"

	"github.com/influxdata/influxdb/pkg/testing/assert"
)

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme:   "https",
		Host:     "blah.com",
		Path:     "/",
		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	fmt.Println(res.Extent)
	if err != nil {
		fmt.Println(err.Error())
	} else {
		assert.Equal(t, int(res.Step), 15)
		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 6)
	}
}

// func TestParseTimeRangeQueryWithBothTimes(t *testing.T) {
// 	req := &http.Request{URL: &url.URL{
// 		Scheme:   "https",
// 		Host:     "blah.com",
// 		Path:     "/",
// 		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time >= now() - 6h AND time < now() - 3h GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
// 	}}
// 	client := &Client{}
// 	res, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
// 	if err != nil {
// 		fmt.Println(err.Error())
// 	} else {
// 		assert.Equal(t, int(res.Step), 15)
// 		assert.Equal(t, int(res.Extent.End.Sub(res.Extent.Start).Hours()), 3)
// 	}
// }

// func TestParseTimeRangeQueryWithoutNow(t *testing.T) {
// 	req := &http.Request{URL: &url.URL{
// 		Scheme:   "https",
// 		Host:     "blah.com",
// 		Path:     "/",
// 		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time > 2052926911485ms AND time < 52926911486ms GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
// 	}}
// 	client := &Client{}
// 	res, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
// 	if err != nil {
// 		fmt.Println(err.Error())
// 	} else {
// 		assert.Equal(t, int(res.Step), 15)
// 		assert.Equal(t, res.Extent.End.UTC().Second()-res.Extent.Start.UTC().Second(), 1)
// 	}
// }

// func TestParseTimeRangeQueryWithAbsoluteTime(t *testing.T) {
// 	req := &http.Request{URL: &url.URL{
// 		Scheme:   "https",
// 		Host:     "blah.com",
// 		Path:     "/",
// 		RawQuery: url.Values(map[string][]string{"q": []string{`SELECT mean("value") FROM "monthly"."rollup.1min" WHERE ("application" = 'web') AND time < 2052926911486ms GROUP BY time(15s), "cluster" fill(null)`}, "epoch": []string{"ms"}}).Encode(),
// 	}}
// 	client := &Client{}
// 	res, err := client.ParseTimeRangeQuery(&proxy.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
// 	if err != nil {
// 		fmt.Println(err.Error())
// 	} else {
// 		assert.Equal(t, int(res.Step), 15)
// 		assert.Equal(t, res.Extent.Start.UTC().IsZero(), true)
// 	}
// }
