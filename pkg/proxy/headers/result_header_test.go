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

package headers

import (
	"net/http"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestSetResultsHeader(t *testing.T) {
	h := http.Header{}
	SetResultsHeader(h, "test-engine", "test-status", "test-ffstatus",
		timeseries.ExtentList{timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}})
	const expected = "engine=test-engine; status=test-status; fetched=[1000-2000]; ffstatus=test-ffstatus"
	if h.Get(NameTricksterResult) != expected {
		t.Errorf("expected %s got %s", expected, h.Get(NameTricksterResult))
	}
}

func TestSetResultsHeaderEmtpy(t *testing.T) {
	h := http.Header{}
	SetResultsHeader(h, "", "test-status", "test-ffstatus",
		timeseries.ExtentList{timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}})
	if len(h) > 0 {
		t.Errorf("Expected header length of %d", 0)
	}
}

func TestMergeResultHeaderVals(t *testing.T) {

	const h1 = "status=kmiss; ffstatus=kmiss"
	const h2 = "engine=ObjectProxyCache; status=phit; fetched=[1612804980000-1612804980000]; ffstatus=hit"
	const ex2 = "engine=ObjectProxyCache; status=phit; fetched=[1612804980000-1612804980000]; ffstatus=phit"

	if res := MergeResultHeaderVals("", h2); res != h2 {
		t.Errorf("unexpected merged header: %s", res)
	}

	if res := MergeResultHeaderVals("x", h2); res != h2 {
		t.Errorf("unexpected merged header: %s", res)
	}

	if res := MergeResultHeaderVals(h1, h2); res != ex2 {
		t.Errorf("unexpected merged header: %s", res)
	}

	if res := MergeResultHeaderVals(h2, h2); res != h2 {
		t.Errorf("unexpected merged header: %s", res)
	}

}

func TestParseResultHeaderVals(t *testing.T) {

	const h1 = "engine=ObjectProxyCache; status=phit; fetched=[aaa-bbb]; ffstatus=hit"
	const h2 = "engine=ObjectProxyCache; status=phit; fetched=[11-bbb]; ffstatus=hit"

	const expected = "engine=ObjectProxyCache; status=phit; ffstatus=hit"
	res := parseResultHeaderVals(h1).String()

	if res != expected {
		t.Errorf("unexpected parsed header: %s", res)
	}

	res = parseResultHeaderVals(h2).String()
	if res != expected {
		t.Errorf("unexpected parsed header: %s", res)
	}

	// t.Error()

}
