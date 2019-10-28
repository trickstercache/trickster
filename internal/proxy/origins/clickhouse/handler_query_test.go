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

package clickhouse

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/Comcast/trickster/internal/proxy/model"
)

func TestParseTimeRangeQuery(t *testing.T) {
	req := &http.Request{URL: &url.URL{
		Scheme: "https",
		Host:   "blah.com",
		Path:   "/",
		RawQuery: url.Values(map[string][]string{"query": []string{
			`SELECT (intDiv(toUInt32(time_column), 60) * 60) * 1000 AS t, countMerge(some_count) AS cnt, field1, field2 ` +
				`FROM testdb.test_table WHERE time_column BETWEEN toDateTime(1516665600) AND toDateTime(1516687200) ` +
				`AND date_column >= toDate(1516665600) AND toDate(1516687200) ` +
				`AND field1 > 0 AND field2 = 'some_value' GROUP BY t, field1, field2 ORDER BY t, field1 FORMAT JSON`}}).Encode(),
	}}
	client := &Client{}
	res, err := client.ParseTimeRangeQuery(&model.Request{ClientRequest: req, URL: req.URL, TemplateURL: req.URL})
	if err != nil {
		t.Error(err)
	} else {

		if res.Step.Seconds() != 60 {
			t.Errorf("expeced 60 got %f", res.Step.Seconds())
		}

		if res.Extent.End.Sub(res.Extent.Start).Hours() != 6 {
			t.Errorf("expeced 6 got %f", res.Extent.End.Sub(res.Extent.Start).Hours())
		}

	}
}
