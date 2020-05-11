/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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
)

func TestIsValidForwardingType(t *testing.T) {

	b := IsValidForwardingType("fail")
	if b {
		t.Error("expected false")
	}

	b = IsValidForwardingType("x")
	if !b {
		t.Error("expected true")
	}

}

var testFD1 = &ForwardedData{
	RemoteAddr: "1.2.3.4",
	Scheme:     "https",
	Host:       "bar.com",
	Server:     "barServer",
	Protocol:   "HTTP/2",
}

var testFD2 = &ForwardedData{
	RemoteAddr: "5.6.7.8",
	Scheme:     "http",
	Host:       "foo.com",
	Server:     "fooServer",
	Protocol:   "HTTP/1.1",
}

func TestForwardedString(t *testing.T) {

	var fd = &*testFD1
	fd.Hops = []*ForwardedData{testFD2}

	setVia(nil)

	s := fd.String()
	expected := "by=fooServer;for=5.6.7.8;host=foo.com;proto=http, by=barServer;for=1.2.3.4;host=bar.com;proto=https"

	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}

}

func TestFdFromRequest(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://bar.com/", nil)
	r.Header = nil
	r.RemoteAddr = "1.2.3.4:5678"
	r.ProtoMajor = 2
	fd := fdFromRequest(r)
	if fd.RemoteAddr != testFD1.RemoteAddr {
		t.Errorf("expected %s got %s", testFD1.RemoteAddr, fd.RemoteAddr)
	}
}

func TestSetVia(t *testing.T) {

	r, _ := http.NewRequest("GET", "https://bar.com/", nil)
	r.Header = nil
	r.RemoteAddr = "1.2.3.4:5678"
	r.ProtoMajor = 2
	SetVia(r, nil)
	if r.Header != nil {
		t.Error("expected nil")
	}
	r.Header = make(http.Header)
	SetVia(r, nil)
	if _, ok := r.Header[NameVia]; !ok {
		t.Error("expected Via header to be set")
	}

}

func TestAddForwardingHeaders(t *testing.T) {

	r, _ := http.NewRequest("GET", "https://bar.com/", nil)
	r.RemoteAddr = "1.2.3.4:5678"
	r.ProtoMajor = 2
	AddForwardingHeaders(r, r, "none")
	if _, ok := r.Header[NameXForwardedFor]; ok {
		t.Error("did not expect X-Forwarded-For header to be set")
	}
	AddForwardingHeaders(r, r, "x")
	if _, ok := r.Header[NameXForwardedFor]; !ok {
		t.Error("expected X-Forwarded-For header to be set")
	}

}

func TestXHeader(t *testing.T) {
	h := testFD1.XHeader()
	if _, ok := h[NameXForwardedFor]; !ok {
		t.Error("expected via X-Forwarded-For header to be set")
	}
}

func TestAddForwarded(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://bar.com/", nil)
	AddForwarded(r, testFD1)
	if _, ok := r.Header[NameForwarded]; !ok {
		t.Error("expected Forwarded header to be set")
	}
}

func TestAddForwardedAndX(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://bar.com/", nil)

	AddForwardedAndX(r, testFD1)
	if _, ok := r.Header[NameForwarded]; !ok {
		t.Error("expected Forwarded header to be set")
	}
	if _, ok := r.Header[NameXForwardedFor]; !ok {
		t.Error("expected via X-Forwarded-For header to be set")
	}
}
