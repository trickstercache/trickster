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

package prometheus

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request"
	tu "github.com/trickstercache/trickster/v2/pkg/testutil"
)

func newAlertsTestClient(t *testing.T) (*Client, *request.Resources, *http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	backendClient, err := NewClient("test", nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts, w, r, _, err := tu.NewTestInstance("", backendClient.DefaultPathConfigs, 200,
		`{"status":"success","data":{"alerts":[]}}`, nil,
		providers.Prometheus, "/api/v1/alerts", "debug")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ts.Close)
	rsc := request.GetResources(r)
	backendClient, err = NewClient("test", rsc.BackendOptions, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := backendClient.(*Client)
	rsc.BackendClient = client
	rsc.BackendOptions.HTTPClient = backendClient.HTTPClient()
	return client, rsc, r, w
}

func TestAlertsHandler_MergeMember_WiresFuncsAndProxies(t *testing.T) {
	client, rsc, r, w := newAlertsTestClient(t)
	rsc.IsMergeMember = true

	client.AlertsHandler(w, r)

	if rsc.MergeFunc == nil {
		t.Error("expected MergeFunc to be set for merge members")
	}
	if rsc.MergeRespondFunc == nil {
		t.Error("expected MergeRespondFunc to be set for merge members")
	}
	if rsc.Response == nil {
		t.Error("expected upstream Response to be captured on rsc")
	}
}

func TestAlertsHandler_NotMergeMember_LeavesFuncsNil(t *testing.T) {
	client, rsc, r, w := newAlertsTestClient(t)

	client.AlertsHandler(w, r)

	if rsc.MergeFunc != nil {
		t.Error("expected MergeFunc to remain nil when not a merge member")
	}
	if rsc.MergeRespondFunc != nil {
		t.Error("expected MergeRespondFunc to remain nil when not a merge member")
	}
}
