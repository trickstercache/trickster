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

package params

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
)

func TestUpdateParams(t *testing.T) {

	params := url.Values{"param1": {"value1"}, "param3": {"value3"}, "param4": {"value4"}}
	updates := map[string]string{"param2": "value2", "+param3": "value3.1", "-param4": "", "": "empty_key_ignored"}
	expected := url.Values{"param1": {"value1"}, "param2": {"value2"}, "param3": {"value3", "value3.1"}}

	UpdateParams(params, nil)
	if len(params) != 3 {
		t.Errorf("expected %d got %d", 1, len(params))
	}

	UpdateParams(params, map[string]string{})
	if len(params) != 3 {
		t.Errorf("expected %d got %d", 1, len(params))
	}

	UpdateParams(params, updates)
	if !reflect.DeepEqual(params, expected) {
		t.Errorf("mismatch\nexpected: %v\n     got: %v\n", expected, params)
	}

}

func TestGetSetRequestValues(t *testing.T) {

	const params = "param1=value1"

	r, _ := http.NewRequest(http.MethodGet, "http://example.com/?"+params, nil)

	v, s, hb := GetRequestValues(r)
	if len(v) != 1 {
		t.Errorf("expected %d got %d", 1, len(v))
	}
	if s != params {
		t.Errorf("expected %s got %s", params, s)
	}
	if hb {
		t.Errorf("expected false")
	}

	v.Set("param2", "value2")
	SetRequestValues(r, v)
	v, s, hb = GetRequestValues(r)
	if len(v) != 2 {
		t.Errorf("expected %d got %d", 2, len(v))
	}
	if s == params || s == "" {
		t.Errorf("expected %s got %s", params+"&param2=value2", s)
	}
	if hb {
		t.Errorf("expected false")
	}

	r, _ = http.NewRequest(http.MethodPost, "http://example.com/", io.NopCloser(bytes.NewBufferString(params)))
	r.Header.Set(headers.NameContentType, headers.ValueXFormURLEncoded)
	v, s, hb = GetRequestValues(r)
	if len(v) != 1 {
		t.Errorf("expected %d got %d", 1, len(v))
	}
	if s != params {
		t.Errorf("expected %s got %s", params, s)
	}
	if !hb {
		t.Errorf("expected true")
	}

	v.Set("param2", "value2")
	SetRequestValues(r, v)
	v, s, hb = GetRequestValues(r)
	if len(v) != 2 {
		t.Errorf("expected %d got %d", 2, len(v))
	}
	if s == params || s == "" {
		t.Errorf("expected %s got %s", params+"&param2=value2", s)
	}
	if !hb {
		t.Errorf("expected true")
	}

	r, _ = http.NewRequest(http.MethodPost, "http://example.com/", io.NopCloser(bytes.NewBufferString(params)))
	r.Header.Set(headers.NameContentType, headers.ValueApplicationJSON)
	v, s, hb = GetRequestValues(r)
	if len(v) != 0 {
		t.Errorf("expected %d got %d", 1, len(v))
	}
	if s != params {
		t.Errorf("expected %s got %s", params, s)
	}
	if !hb {
		t.Errorf("expected true")
	}
}
