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

package proxy

import (
	"net/http"
	"testing"

	"github.com/Comcast/trickster/internal/config"
)

func TestAddProxyHeaders(t *testing.T) {

	headers := http.Header{}
	config.ApplicationName = "trickster-test"
	config.ApplicationVersion = "tests"

	addProxyHeaders("0.0.0.0", headers)

	if _, ok := headers[hnXForwardedFor]; !ok {
		t.Errorf("missing header %s", hnXForwardedFor)
	}

	if _, ok := headers[hnXForwardedBy]; !ok {
		t.Errorf("missing header %s", hnXForwardedBy)
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

	addProxyHeaders(testIP, headers)

	if h, ok := extractHeader(headers, hnXForwardedFor); !ok {
		t.Errorf("missing header %s", hnXForwardedFor)
	} else {
		if h != testIP {
			t.Errorf(`wanted "%s". got "%s"`, testIP, h)
		}
	}

	if h, ok := extractHeader(headers, hnXForwardedBy); !ok {
		t.Errorf("missing header %s", hnXForwardedBy)
	} else {
		if h != appString {
			t.Errorf(`wanted "%s". got "%s"`, appString, h)
		}
	}

	if _, ok := extractHeader(headers, hnAllowOrigin); ok {
		t.Errorf("unexpected header %s", hnAllowOrigin)
	}

}

func TestRemoveClientHeader(t *testing.T) {

	headers := http.Header{}
	headers.Set(hnAcceptEncoding, "test")

	removeClientHeaders(headers)

	if _, ok := extractHeader(headers, hnAcceptEncoding); ok {
		t.Errorf("unexpected header %s", hnAcceptEncoding)
	}

}
