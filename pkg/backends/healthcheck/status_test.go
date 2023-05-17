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

package healthcheck

import (
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestString(t *testing.T) {
	tm := time.Unix(0, 0)
	status := &Status{
		name:         "test",
		description:  "test-description",
		detail:       "status-detail",
		failingSince: tm,
	}
	status.status.Store(-1)
	const expected = "target: test\nstatus: -1\ndetail: status-detail\nsince: 0"
	s := status.String()
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}

func TestHeaders(t *testing.T) {

	const expectedDetail = "status-detail"
	const expectedStatus = -1
	const expectedStatusStr = "-1"

	status := &Status{
		detail: expectedDetail,
	}
	status.RegisterSubscriber(make(chan bool, 1))
	status.Set(expectedStatus)

	h := status.Headers()
	v := h.Get(headers.NameTrkHCStatus)
	if v != expectedStatusStr {
		t.Error("invalid status", v)
	}
}

func TestProber(t *testing.T) {

	status := &Status{}
	if status.Prober() != nil {
		t.Error("expected nil prober")
	}
}

func TestGet(t *testing.T) {

	status := &Status{}
	status.status.Store(8480)
	if status.Get() != 8480 {
		t.Error("expected 8480 got", status.Get())
	}
}

func TestDetail(t *testing.T) {

	status := &Status{detail: "trickster"}
	if status.Detail() != "trickster" {
		t.Error("expected trickster got", status.Detail())
	}
}

func TestDescription(t *testing.T) {

	status := &Status{description: "trickster"}
	if status.Description() != "trickster" {
		t.Error("expected trickster got", status.Description())
	}
}

func TestFailingSince(t *testing.T) {
	tm := time.Unix(0, 0)
	status := &Status{failingSince: tm}
	if status.FailingSince() != tm {
		t.Error("expected 0 got", status.FailingSince().Unix())
	}
}
