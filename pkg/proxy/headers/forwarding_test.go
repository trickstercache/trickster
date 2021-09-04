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

var testHops1 = &Hop{
	RemoteAddr: "1.2.3.4",
	Scheme:     "https",
	Host:       "bar.com",
	Server:     "barServer",
	Protocol:   "HTTP/2",
}

var testHops2 = &Hop{
	RemoteAddr: "5.6.7.8",
	Scheme:     "http",
	Host:       "foo.com",
	Server:     "fooServer",
	Protocol:   "HTTP/1.1",
}

func TestForwardedString(t *testing.T) {

	var hop = testHops1
	hop.Hops = []*Hop{testHops2}

	s := hop.String()
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
	hop := HopsFromRequest(r)
	if hop.RemoteAddr != testHops1.RemoteAddr {
		t.Errorf("expected %s got %s", testHops1.RemoteAddr, hop.RemoteAddr)
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
	SetVia(r, &Hop{Via: "1.1 test"})
	if _, ok := r.Header[NameVia]; !ok {
		t.Error("expected Via header to be set")
	}

}

func TestAddForwardingHeaders(t *testing.T) {

	r, _ := http.NewRequest("GET", "https://bar.com/", nil)
	r.RemoteAddr = "1.2.3.4:5678"
	r.ProtoMajor = 2
	AddForwardingHeaders(nil, "none")
	if _, ok := r.Header[NameXForwardedFor]; ok {
		t.Error("did not expect X-Forwarded-For header to be set")
	}
	AddForwardingHeaders(r, "none")
	if _, ok := r.Header[NameXForwardedFor]; ok {
		t.Error("did not expect X-Forwarded-For header to be set")
	}
	AddForwardingHeaders(r, "x")
	if _, ok := r.Header[NameXForwardedFor]; !ok {
		t.Error("expected X-Forwarded-For header to be set")
	}

}

func TestXHeader(t *testing.T) {
	h := testHops1.XHeader()
	if _, ok := h[NameXForwardedFor]; !ok {
		t.Error("expected via X-Forwarded-For header to be set")
	}
}

func TestAddForwarded(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://bar.com/", nil)
	AddForwarded(r, testHops1)
	if _, ok := r.Header[NameForwarded]; !ok {
		t.Error("expected Forwarded header to be set")
	}
}

func TestAddForwardedAndX(t *testing.T) {
	r, _ := http.NewRequest("GET", "https://bar.com/", nil)

	AddForwardedAndX(r, testHops1)
	if _, ok := r.Header[NameForwarded]; !ok {
		t.Error("expected Forwarded header to be set")
	}
	if _, ok := r.Header[NameXForwardedFor]; !ok {
		t.Error("expected via X-Forwarded-For header to be set")
	}
}

func TestHopsFromHeader(t *testing.T) {

	h := http.Header{}
	hops := HopsFromHeader(h)
	if hops != nil {
		t.Error("expected nil Hops")
	}

	h.Set(NameForwarded, "by=foo;for=bar;host=downstream;proto=localhost")
	hops = HopsFromHeader(h)
	if hops == nil {
		t.Error("expected non-nil Hops")
	}

	h.Del(NameForwarded)
	h.Set(NameXForwardedFor, "server-before-bar, bar")
	hops = HopsFromHeader(h)
	if hops == nil {
		t.Error("expected non-nil Hops")
	}

	if len(hops) != 2 {
		t.Errorf("expected %d got  %d", 2, len(hops))
	}

}

func TestParseXForwardHeaders(t *testing.T) {
	hops := parseXForwardHeaders(nil)
	if hops != nil {
		t.Error("expected nil Hops")
	}
}

func TestFormatForwardedAddress(t *testing.T) {
	s := "::FFFF:192.168.0.1"
	expected := `["::FFFF:192.168.0.1"]`
	s = formatForwardedAddress(s)
	if s != expected {
		t.Errorf("expected %s got %s", expected, s)
	}
}

func TestStripMergeHeaders(t *testing.T) {

	h := http.Header{NameContentLength: []string{"42"}, NameLocation: []string{"https://trickstercache.org/"}}
	StripMergeHeaders(h)

	if _, ok := h[NameContentLength]; ok {
		t.Error("expected Content-Length Header to be missing")
	}

	if _, ok := h[NameLocation]; !ok {
		t.Error("expected Location Header to remain present")
	}

}
