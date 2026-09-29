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

package testutil

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
)

func TestRecordingTransport(t *testing.T) {
	ts := NewTestServer(http.StatusOK, "ok", nil)
	defer ts.Close()
	rt := &RecordingTransport{}
	c := &http.Client{Transport: rt}
	for _, r := range []*http.Request{
		mustRequest(t, http.MethodGet, ts.URL+"/a?x=1", nil),
		mustRequest(t, http.MethodPost, ts.URL+"/b", strings.NewReader("body")),
	} {
		resp, err := c.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := io.ReadAll(resp.Body); string(b) != "ok" {
			t.Errorf("the request did not reach the origin: %s", b)
		}
		resp.Body.Close()
	}
	got := rt.Take()
	if len(got) != 2 || got[0].Method != http.MethodGet || got[0].URL.Query().Get("x") != "1" ||
		got[1].Method != http.MethodPost || string(got[1].Body) != "body" {
		t.Errorf("recorded %+v", got)
	}
	if len(rt.Take()) != 0 {
		t.Error("Take should clear the recording")
	}
	// a body that can't be read never reaches the origin
	_, err := (&RecordingTransport{}).RoundTrip(mustRequest(t, http.MethodPost, ts.URL,
		iotest.ErrReader(errors.New("unreadable"))))
	if err == nil {
		t.Error("expected the body's read error")
	}
}

func mustRequest(t *testing.T, method, u string, body io.Reader) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, u, body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
