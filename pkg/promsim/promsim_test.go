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

package promsim

import (
	"fmt"
	"testing"
	"time"
)

func TestGetTimeSeriesData(t *testing.T) {
	fmt.Println(GetTimeSeriesData("myQuery{other_label=5,latency_ms=0,range_latency_ms=0,series_count=2,test}", time.Unix(0, 0), time.Unix(3600, 0), time.Duration(60)*time.Second))
}
