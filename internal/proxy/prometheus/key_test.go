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

package prometheus

import (
	"net/url"
	"testing"

	"github.com/Comcast/trickster/internal/proxy"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestDeriveCacheKey(t *testing.T) {

	client := &Client{}
	u := &url.URL{Path: "/", RawQuery: "query=12345&start=0&end=0&step=300&time=0"}
	r := &proxy.Request{URL: u, TimeRangeQuery: &timeseries.TimeRangeQuery{Step: 300000}}
	key := client.DeriveCacheKey(r, "extra")

	if key != "6667a75e76dea9a5cd6c6ba73e5825b5" {
		t.Errorf("wanted %s got %s", "6667a75e76dea9a5cd6c6ba73e5825b5", key)
	}

}
