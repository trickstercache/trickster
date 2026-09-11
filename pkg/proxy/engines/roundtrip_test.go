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

package engines

import (
	"bytes"
	"testing"
)

func TestCachingPolicyRoundTrip(t *testing.T) {
	v := CachingPolicy{
		IsFresh:           true,
		NoCache:           true,
		CanRevalidate:     true,
		FreshnessLifetime: 300,
		ETag:              "abc123",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 CachingPolicy
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if !v2.IsFresh {
		t.Fatal("IsFresh mismatch")
	}
	if !v2.NoCache {
		t.Fatal("NoCache mismatch")
	}
	if !v2.CanRevalidate {
		t.Fatal("CanRevalidate mismatch")
	}
	if v2.FreshnessLifetime != 300 {
		t.Fatal("FreshnessLifetime mismatch")
	}
	if v2.ETag != "abc123" {
		t.Fatal("ETag mismatch")
	}
}

func TestHTTPDocumentRoundTrip(t *testing.T) {
	v := HTTPDocument{
		StatusCode:  200,
		Status:      "200 OK",
		Body:        []byte("response body"),
		ContentType: "application/json",
	}
	b, err := v.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	var v2 HTTPDocument
	_, err = v2.UnmarshalMsg(b)
	if err != nil {
		t.Fatal(err)
	}
	if v2.StatusCode != 200 {
		t.Fatal("StatusCode mismatch")
	}
	if v2.Status != "200 OK" {
		t.Fatal("Status mismatch")
	}
	if !bytes.Equal(v2.Body, []byte("response body")) {
		t.Fatal("Body mismatch")
	}
	if v2.ContentType != "application/json" {
		t.Fatal("ContentType mismatch")
	}
}
