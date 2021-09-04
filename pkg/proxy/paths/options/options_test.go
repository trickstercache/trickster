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

package options

import (
	"net/http"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/forwarding"
	"github.com/trickstercache/trickster/v2/pkg/proxy/paths/matching"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"
)

func TestNew(t *testing.T) {

	pc := New()

	if pc == nil {
		t.Errorf("expected non-nil value you for %s", "PathConfig")
	}

	if pc.HandlerName != "proxy" {
		t.Errorf("expected value %s, got %s", "proxy", pc.HandlerName)
	}

}

func TestPathClone(t *testing.T) {

	pc := New()
	pc2 := pc.Clone()

	if pc2 == nil {
		t.Errorf("expected non-nil value you for %s", "PathConfig")
	}

	if pc2.HandlerName != "proxy" {
		t.Errorf("expected value %s, got %s", "proxy", pc2.HandlerName)
	}

}

func TestPathMerge(t *testing.T) {

	pc := New()
	pc2 := pc.Clone()

	pc2.Custom = []string{"path", "match_type", "handler", "methods",
		"cache_key_params", "cache_key_headers", "cache_key_form_fields",
		"request_headers", "request_params", "response_headers",
		"response_code", "response_body", "no_metrics", "collapsed_forwarding"}

	expectedPath := "testPath"
	expectedHandlerName := "testHandler"

	pc2.Path = expectedPath
	pc2.MatchType = matching.PathMatchTypePrefix
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
	pc2.CollapsedForwardingType = forwarding.CFTypeProgressive

	pc.Merge(pc2)

	if pc.Path != expectedPath {
		t.Errorf("expected %s got %s", expectedPath, pc.Path)
	}

	if pc.MatchType != matching.PathMatchTypePrefix {
		t.Errorf("expected %s got %s", matching.PathMatchTypePrefix, pc.MatchType)
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

	if pc.CollapsedForwardingName != "progressive" ||
		pc.CollapsedForwardingType != forwarding.CFTypeProgressive {
		t.Errorf("expected %s got %s", "progressive", pc.CollapsedForwardingName)
	}

}

func TestMerge(t *testing.T) {

	o := &Options{}
	o2 := &Options{Custom: []string{"req_rewriter_name"}}
	o.Merge(o2)

	if len(o.Custom) != 1 {
		t.Errorf("expected %d got %d", 1, len(o.Custom))
	}

}

func TestSetDefaults(t *testing.T) {

	err := SetDefaults("default", nil, nil, nil)
	if err != errInvalidConfigMetadata {
		t.Error("expected errInvalidConfigMetadata, got", err)
	}

	kl, err := yamlx.GetKeyList(testYAML)
	if err != nil {
		t.Error(err)
	}

	o := New()
	pl := Lookup{"root": o}
	o.ReqRewriterName = "path"
	o.ResponseBody = "trickster"
	o.Methods = nil
	crw := map[string]rewriter.RewriteInstructions{"path": nil}

	err = SetDefaults("test", kl, pl, crw)
	if err != nil {
		t.Error(err)
	}

	o.ReqRewriterName = "invalid"
	err = SetDefaults("test", kl, pl, crw)
	if err == nil {
		t.Error("expected error for invalid rewriter name")
	}

	o.ReqRewriterName = "path"
	o.MatchTypeName = "invalid"
	err = SetDefaults("test", kl, pl, crw)
	if err != nil {
		t.Error(err)
	}

	o.CollapsedForwardingName = "invalid"
	err = SetDefaults("test", kl, pl, crw)
	if err == nil {
		t.Error("expected error for invalid collapsed_forwarding name")
	}
}

const testYAML = `
request_rewriters:
  path:
    instructions:
      - - header
        - set
        - Test-Path
        - pass
  origin:
    instructions:
      - - header
        - set
        - Test-Origin
        - pass
  ingress:
    instructions:
      - - header
        - set
        - Test-Ingress
        - pass
  egress:
    instructions:
      - - header
        - set
        - Test-Egress
        - pass
  default:
    instructions:
      - - header
        - set
        - Test-Default
        - pass
  match:
    instructions:
      - - header
        - set
        - Test-Match
        - pass
backends:
  test:
    provider: rpc
    origin_url: 'http://1'
    req_rewriter_name: origin
    paths:
      root:
        path: /
        req_rewriter_name: path
        handler: proxycache
        response_body: trickster
        collapsed_forwarding: progressive
`
