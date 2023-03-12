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
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
)

func testNegativeCaches() negative.Lookups {

	m := make(negative.Lookups)
	m["test"] = negative.Lookup{}

	return m
}

const testYAML = `
backends:
  test:
    tracing_name: test
    hosts:
      - 1.example.com
    revalidation_factor: 2
    multipart_ranges_disabled: true
    dearticulate_upstream_ranges: true
    compressible_types:
      - image/png
    provider: test_type
    cache_name: test
    origin_url: 'scheme://test_host/test_path_prefix'
    api_path: test_api_path
    max_idle_conns: 23
    keep_alive_timeout_ms: 7000
    ignore_caching_headers: true
    timeseries_retention_factor: 666
    timeseries_eviction_method: lru
    fast_forward_disable: true
    backfill_tolerance_ms: 301000
    backfill_tolerance_points: 2
    timeout_ms: 37000
    timeseries_ttl_ms: 8666000
    max_ttl_ms: 300000
    fastforward_ttl_ms: 382000
    require_tls: true
    max_object_size_bytes: 999
    cache_key_prefix: test-prefix
    path_routing_disabled: false
    forwarded_headers: x
    negative_cache_name: test
    rule_name: ''
    shard_max_size_ms: 0
    shard_max_size_points: 0
    shard_step_ms: 0
    healthcheck:
      headers:
        Authorization: Basic SomeHash
      path: /test/upstream/endpoint
      verb: test_verb
      query: query=1234
    paths:
      series:
        path: /series
        handler: proxy
        req_rewriter_name: ''
      label:
        path: /label
        handler: localresponse
        match_type: prefix
        response_code: 200
        response_body: test
        collapsed_forwarding: basic
        response_headers:
          X-Header-Test: test-value
    prometheus:
      labels:
        testlabel: trickster
    alb:
      methodology: rr
      pool: [ test ]
    tls:
      full_chain_cert_path: file.that.should.not.exist.ever.pem
      private_key_path: file.that.should.not.exist.ever.pem
      insecure_skip_verify: true
      certificate_authority_paths:
        - file.that.should.not.exist.ever.pem
      client_key_path: test_client_key
      client_cert_path: test_client_cert

`

func fromTestYAML() (*Options, error) {
	return fromYAML(testYAML)
}

func fromTestYAMLWithDefault() (*Options, error) {
	conf := strings.Replace(testYAML, "    rule_name: ''", "    rule_name: ''\n    is_default: false", -1)
	return fromYAML(conf)
}

func fromTestYAMLWithPath() (*Options, error) {
	conf := strings.Replace(testYAML, "    rule_name: ''", "    rule_name: ''\n    is_default: false", -1)
	return fromYAML(conf)
}

func fromTestYAMLWithReqRewriter() (*Options, error) {
	conf := strings.Replace(testYAML, "    rule_name: ''", "    rule_name: ''\n    req_rewriter_name: test", -1)
	return fromYAML(conf)
}

func fromTestYAMLWithALB() (*Options, error) {
	conf := strings.Replace(strings.Replace(testYAML, "    rule_name: ''", `
    rule_name: ''
    alb:
      output_format: prometheus
      mechanism: tsmerge
        `, -1), "    provider: test_type", "    provider: 'alb'", -1)
	return fromYAML(conf)
}
