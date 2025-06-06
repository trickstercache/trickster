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

package influxdb

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

const untokenized = "SELECT * FROM some_column WHERE time >= now() - 6h GROUP BY time(1m)"

func TestParseTimeRangeQuery(t *testing.T) {
	url := fmt.Sprintf("http://example.com/?q=%s",
		url.QueryEscape(untokenized))
	r, _ := http.NewRequest(http.MethodGet, url, nil)
	c := &Client{}
	trq, _, _, err := c.ParseTimeRangeQuery(r)
	if err != nil {
		t.Error(err)
	}
	if trq == nil {
		t.Error("expected non-nil time range query")
	}

}
