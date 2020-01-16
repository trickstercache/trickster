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

package config

import (
	"net/http"
	"testing"
)

func TestPMTString(t *testing.T) {

	t1 := PathMatchTypeExact
	t2 := PathMatchTypePrefix

	var t3 PathMatchType = 3

	if t1.String() != "exact" {
		t.Errorf("expected %s got %s", "exact", t1.String())
	}

	if t2.String() != "prefix" {
		t.Errorf("expected %s got %s", "prefix", t2.String())
	}

	if t3.String() != "3" {
		t.Errorf("expected %s got %s", "3", t3.String())
	}

}

func TestNewPathConfig(t *testing.T) {

	pc := NewPathConfig()

	if pc == nil {
		t.Errorf("expected non-nil value you for %s", "PathConfig")
	}

	if pc.HandlerName != "proxy" {
		t.Errorf("expected value %s, got %s", "proxy", pc.HandlerName)
	}

}

func TestPathCopy(t *testing.T) {

	pc := NewPathConfig()
	pc2 := pc.Copy()

	if pc2 == nil {
		t.Errorf("expected non-nil value you for %s", "PathConfig")
	}

	if pc2.HandlerName != "proxy" {
		t.Errorf("expected value %s, got %s", "proxy", pc2.HandlerName)
	}

}

func TestPathMerge(t *testing.T) {

	pc := NewPathConfig()
	pc2 := pc.Copy()

	pc2.OriginConfig = NewOriginConfig()

	pc2.custom = []string{"path", "match_type", "handler", "methods", "cache_key_params", "cache_key_headers", "cache_key_form_fields",
		"request_headers", "request_params", "response_headers", "response_code", "response_body", "no_metrics", "collapsed_forwarding"}

	expectedPath := "testPath"
	expectedHandlerName := "testHandler"

	pc2.Path = expectedPath
	pc2.MatchType = PathMatchTypePrefix
	pc2.HandlerName = expectedHandlerName
	pc2.Methods = []string{http.MethodPost}
	pc2.CacheKeyParams = []string{"params"}
	pc2.CacheKeyHeaders = []string{"headers"}
	pc2.CacheKeyFormFields = []string{"fields"}
	pc2.RequestHeaders = map[string]string{"header1": "1"}
	pc2.RequestParams = map[string]string{"param1": "foo"}
	pc2.ResponseHeaders = map[string]string{"header2": "2"}
	pc2.ResponseCode = 404
	pc2.ResponseBody = "trickster"
	pc2.NoMetrics = true
	pc2.CollapsedForwardingName = "progressive"
	pc2.CollapsedForwardingType = CFTypeProgressive

	pc.Merge(pc2)

	if pc.Path != expectedPath {
		t.Errorf("expected %s got %s", expectedPath, pc.Path)
	}

	if pc.MatchType != PathMatchTypePrefix {
		t.Errorf("expected %s got %s", PathMatchTypePrefix, pc.MatchType)
	}

	if pc.HandlerName != expectedHandlerName {
		t.Errorf("expected %s got %s", expectedHandlerName, pc.HandlerName)
	}

	if len(pc.CacheKeyParams) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.CacheKeyParams))
	}

	if len(pc.CacheKeyHeaders) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.CacheKeyHeaders))
	}

	if len(pc.CacheKeyFormFields) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.CacheKeyFormFields))
	}

	if len(pc.RequestHeaders) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.RequestHeaders))
	}

	if len(pc.RequestParams) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.RequestParams))
	}

	if len(pc.ResponseHeaders) != 1 {
		t.Errorf("expected %d got %d", 1, len(pc.ResponseHeaders))
	}

	if pc.ResponseCode != 404 {
		t.Errorf("expected %d got %d", 404, pc.ResponseCode)
	}

	if pc.ResponseCode != 404 {
		t.Errorf("expected %d got %d", 404, pc.ResponseCode)
	}

	if pc.ResponseBody != "trickster" {
		t.Errorf("expected %s got %s", "trickster", pc.ResponseBody)
	}

	if !pc.NoMetrics {
		t.Errorf("expected %t got %t", true, pc.NoMetrics)
	}

	if pc.OriginConfig == nil {
		t.Errorf("expected non-nil value you for %s", "OriginConfig")
	}

	if pc.CollapsedForwardingName != "progressive" || pc.CollapsedForwardingType != CFTypeProgressive {
		t.Errorf("expected %s got %s", "progressive", pc.CollapsedForwardingName)
	}

}
