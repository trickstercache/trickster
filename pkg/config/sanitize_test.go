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

package config

import (
	"strings"
	"testing"
)

func TestSanitizedString(t *testing.T) {
	conf := NewConfig()
	err := conf.loadYAMLConfig(`
caches:
  cache-a:
    provider: memory
  cache-b:
    provider: memory
authenticators:
  auth-a:
    provider: basic
    users:
      alice: secret-a
      bob: secret-b
  auth-z:
    provider: basic
    users:
      charlie: secret-c
tracing:
  traces-a:
    provider: otlp
    endpoint: http://traces-a.private.example:4318/v1/traces
  traces-b:
    provider: otlp
    endpoint: traces-b.private.example:4317
  traces-stdout:
    provider: stdout
backends:
  alb-main:
    provider: alb
    alb:
      pool:
        - prom-a
        - prom-b
  alb-users:
    provider: alb
    alb:
      user_router:
        default_backend: prom-a
        users:
          user-a:
            to_backend: prom-b
            to_user: upstream-user
            to_credential: upstream-credential
  prom-a:
    provider: prometheus
    origin_url: http://prom-a.private.example:9090/private/path
    cache_name: cache-a
    authenticator_name: auth-z
    tracing_name: traces-b
    paths:
      - path: /query
        authenticator_name: auth-a
        request_headers:
          X-Org-ID: private-org
        response_headers:
          X-Environment: private-env
      - path: /public
        authenticator_name: none
  prom-b:
    provider: prometheus
    origin_url: http://prom-b.private.example:9090/private/path
    cache_name: cache-b
    tracing_name: traces-a
  rule-main:
    provider: rule
    rule_name: route-rule
    tracing_name: traces-stdout
rules:
  route-rule:
    next_route: alb-main
    cases:
      - matches:
          - a
        next_route: prom-b
`)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	out := conf.SanitizedString()

	for _, want := range []string{
		"alb-1:",
		"alb-2:",
		"prom-1:",
		"prom-2:",
		"rule-1:",
		"memory-1:",
		"memory-2:",
		"auth1:",
		"auth2:",
		"authenticator_name: auth2",
		"authenticator_name: auth1",
		"authenticator_name: none",
		"user1: redacted",
		"user2: redacted",
		"otlp-1:",
		"otlp-2:",
		"stdout-1:",
		"tracing_name: otlp-2",
		"tracing_name: otlp-1",
		"tracing_name: stdout-1",
		"endpoint: example.com",
		"origin_url: example.com",
		"cache_name: memory-1",
		"cache_name: memory-2",
		"- prom-1",
		"- prom-2",
		"default_backend: prom-1",
		"to_backend: prom-2",
		"next_route: alb-1",
		"next_route: prom-2",
		"X-Org-ID: '*****'",
		"X-Environment: '*****'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected sanitized config to contain %q; got:\n%s", want, out)
		}
	}

	for _, privateValue := range []string{
		"alb-main",
		"alb-users",
		"prom-a",
		"prom-b",
		"rule-main",
		"cache-a",
		"cache-b",
		"auth-a",
		"auth-z",
		"alice",
		"bob",
		"charlie",
		"secret-a",
		"secret-b",
		"secret-c",
		"traces-a",
		"traces-b",
		"traces-stdout",
		"traces-a.private.example",
		"traces-b.private.example",
		"prom-a.private.example",
		"prom-b.private.example",
		"private-org",
		"private-env",
	} {
		if strings.Contains(out, privateValue) {
			t.Errorf("expected sanitized config not to contain %q; got:\n%s", privateValue, out)
		}
	}

	if conf.Backends["prom-a"].CacheName != "cache-a" {
		t.Errorf("expected original backend cache name to remain unchanged")
	}
	if conf.Backends["prom-a"].Paths[0].RequestHeaders["X-Org-ID"] != "private-org" {
		t.Errorf("expected original path request header to remain unchanged")
	}
	if conf.Backends["prom-a"].AuthenticatorName != "auth-z" {
		t.Errorf("expected original backend authenticator reference to remain unchanged")
	}
	if conf.Backends["prom-a"].Paths[0].AuthenticatorName != "auth-a" {
		t.Errorf("expected original path authenticator reference to remain unchanged")
	}
	if conf.Authenticators["auth-a"].Users["alice"] != "secret-a" {
		t.Errorf("expected original authenticator users to remain unchanged")
	}
	if conf.Backends["prom-a"].TracingConfigName != "traces-b" {
		t.Errorf("expected original backend tracing reference to remain unchanged")
	}
	if conf.TracingOptions["traces-b"].Endpoint != "traces-b.private.example:4317" {
		t.Errorf("expected original tracing endpoint to remain unchanged")
	}
	if conf.Backends["alb-users"].ALBOptions.UserRouter.Users["user-a"].ToBackend != "prom-b" {
		t.Errorf("expected original user router backend reference to remain unchanged")
	}
	if conf.Rules["route-rule"].CaseOptions[0].NextRoute != "prom-b" {
		t.Errorf("expected original rule case backend reference to remain unchanged")
	}
}
