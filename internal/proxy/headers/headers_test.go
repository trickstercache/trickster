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

package headers

import (
	"net/http"
	"testing"
	"time"

	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/timeseries"
)

func TestAddProxyHeaders(t *testing.T) {

	headers := http.Header{}
	config.ApplicationName = "trickster-test"
	config.ApplicationVersion = "tests"

	AddProxyHeaders("0.0.0.0", headers)

	if _, ok := headers[NameXForwardedFor]; !ok {
		t.Errorf("missing header %s", NameXForwardedFor)
	}

	if _, ok := headers[NameXForwardedBy]; !ok {
		t.Errorf("missing header %s", NameXForwardedBy)
	}

}

func TestExtractHeader(t *testing.T) {

	headers := http.Header{}

	const appName = "trickster-test"
	const appVer = "tests"
	const appString = appName + " " + appVer

	config.ApplicationName = appName
	config.ApplicationVersion = appVer

	const testIP = "0.0.0.0"

	AddProxyHeaders(testIP, headers)

	if h, ok := ExtractHeader(headers, NameXForwardedFor); !ok {
		t.Errorf("missing header %s", NameXForwardedFor)
	} else {
		if h != testIP {
			t.Errorf(`expected "%s". got "%s"`, testIP, h)
		}
	}

	if h, ok := ExtractHeader(headers, NameXForwardedBy); !ok {
		t.Errorf("missing header %s", NameXForwardedBy)
	} else {
		if h != appString {
			t.Errorf(`expected "%s". got "%s"`, appString, h)
		}
	}

	if _, ok := ExtractHeader(headers, NameAllowOrigin); ok {
		t.Errorf("unexpected header %s", NameAllowOrigin)
	}

}

func TestRemoveClientHeader(t *testing.T) {

	headers := http.Header{}
	headers.Set(NameAcceptEncoding, "test")

	RemoveClientHeaders(headers)

	if _, ok := ExtractHeader(headers, NameAcceptEncoding); ok {
		t.Errorf("unexpected header %s", NameAcceptEncoding)
	}

}

func TestCopyHeaders(t *testing.T) {
	headers := make(http.Header)
	headers.Set("test", "pass")
	h2 := CopyHeaders(headers)
	if h2.Get("test") != "pass" {
		t.Errorf("expected 'pass' got '%s'", h2.Get("test"))
	}
}

func TestAddResponseHeaders(t *testing.T) {

	headers := http.Header{}
	config.ApplicationName = "trickster-test"
	config.ApplicationVersion = "tests"

	AddResponseHeaders(headers)

	if _, ok := headers[NameAllowOrigin]; !ok {
		t.Errorf("missing header %s", NameAllowOrigin)
	}

	if _, ok := headers[NameXAccelerator]; !ok {
		t.Errorf("missing header %s", NameXAccelerator)
	}

}

func TestSetResultsHeader(t *testing.T) {
	h := http.Header{}
	SetResultsHeader(h, "test-engine", "test-status", "test-ffstatus", timeseries.ExtentList{timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}})
	const expected = "engine=test-engine; status=test-status; fetched=[1:2]; ffstatus=test-ffstatus"
	if h.Get(NameTricksterResult) != expected {
		t.Errorf("expected %s got %s", expected, h.Get(NameTricksterResult))
	}
}

func TestSetResultsHeaderEmtpy(t *testing.T) {
	h := http.Header{}
	SetResultsHeader(h, "", "test-status", "test-ffstatus", timeseries.ExtentList{timeseries.Extent{Start: time.Unix(1, 0), End: time.Unix(2, 0)}})
	if len(h) > 0 {
		t.Errorf("Expected header length of %d", 0)
	}
}
