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

package methods

import (
	"net/http"
	"testing"
)

func TestAllHTTPMethods(t *testing.T) {
	expected := 10
	l := len(AllHTTPMethods())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestGetAndPost(t *testing.T) {
	expected := 2
	l := len(GetAndPost())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestCacheableHTTPMethods(t *testing.T) {
	expected := 2
	l := len(CacheableHTTPMethods())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestUncacheableHTTPMethods(t *testing.T) {
	expected := 8
	l := len(UncacheableHTTPMethods())
	if l != expected {
		t.Errorf("expected %d got %d", expected, l)
	}
}

func TestIsCacheable(t *testing.T) {
	if !IsCacheable(http.MethodGet) {
		t.Error("expected true")
	}
	if IsCacheable(http.MethodPut) {
		t.Error("expected false")
	}
	if IsCacheable("invalid_method") {
		t.Error("expected false")
	}
}

func TestHasBody(t *testing.T) {
	if HasBody(http.MethodGet) {
		t.Error("expected false")
	}
	if !HasBody(http.MethodPut) {
		t.Error("expected true")
	}
	if HasBody("invalid_method") {
		t.Error("expected false")
	}
}

func TestMethodMask(t *testing.T) {
	if v := MethodMask(http.MethodGet); v != 1 {
		t.Errorf("expected 1 got %d", v)
	}
}
