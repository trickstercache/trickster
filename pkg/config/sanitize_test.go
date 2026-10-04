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
	"fmt"
	"strings"
	"testing"

	acmeopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/acme/options"
)

func TestSanitizedString(t *testing.T) {
	conf := NewConfig()
	err := conf.loadYAMLConfig(`
caches:
  cache-a:
    provider: memory
  cache-b:
    provider: memory
  redis-cache:
    provider: redis
    redis:
      endpoint: redis.private.example:6379
      endpoints:
        - redis-a.private.example:6379
        - redis-b.private.example:6379
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
listeners:
  private-listener:
    port: 9000
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
    replica_group: private-ha-shard
    listener_name: private-listener
    origin_url: http://prom-a.private.example:9090/private/path
    cache_name: cache-a
    authenticator_name: auth-z
    tracing_name: traces-b
    paths:
      - path: /query
        authenticator_name: auth-a
        request_headers:
          X-Org-ID: private-org
          '+authorization': Bearer prom-append-secret
          cache-control: no-cache
          EXPIRES: Thu, 01 Jan 1970 00:00:00 GMT
        response_headers:
          X-Environment: private-env
          Cache-Control: max-age=60
          expires: Fri, 02 Jan 1970 00:00:00 GMT
      - path: /public
        authenticator_name: none
  prom-b:
    provider: prometheus
    replica_group: private-ha-shard
    listener_name: private-listener
    origin_url: http://prom-b.private.example:9090/private/path
    cache_name: cache-b
    tracing_name: traces-a
  graphite-main:
    provider: graphite
    origin_url: http://graphite.private.example:8080
    cache_name: cache-a
    graphite:
      origin_username: graphite-user
      origin_password: graphite-secret
    healthcheck:
      headers:
        authorization: Bearer graphite-probe-secret
    paths:
      - path: /render
        request_headers:
          authorization: Bearer graphite-path-secret
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
request_rewriters:
  host-rewriter:
    instructions:
      - [host, set, internal.private.example:9090]
      - [host, replace, old.private.example, new.private.example]
      - [hostname, set, hostname.private.example]
      - [header, set, Host, header.private.example]
      - [header, replace, host, old-header.private.example, new-header.private.example]
      - [header, set, X-Private-Host, should-remain.private.example]
`)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	out := conf.SanitizedString()
	sanitized := conf.SanitizedClone()
	if sanitized.Backends["prom-1"].ReplicaGroup == "" ||
		sanitized.Backends["prom-1"].ReplicaGroup != sanitized.Backends["prom-2"].ReplicaGroup {
		t.Fatalf("equal replica groups were not preserved during sanitization")
	}

	for _, want := range []string{
		"alb-1:",
		"alb-2:",
		"prom-1:",
		"prom-2:",
		"rule-1:",
		"memory-1:",
		"memory-2:",
		"redis-1:",
		"listener-1:",
		"listener_name: listener-1",
		"auth1:",
		"auth2:",
		"authenticator_name: auth2",
		"authenticator_name: auth1",
		"authenticator_name: none",
		"user1: '*****'",
		"user2: '*****'",
		"otlp-1:",
		"otlp-2:",
		"stdout-1:",
		"tracing_name: otlp-2",
		"tracing_name: otlp-1",
		"tracing_name: stdout-1",
		"endpoint: example.com",
		"- example.com",
		"- - host\n      - set\n      - example.com",
		"- - host\n      - replace\n      - example.com\n      - example.com",
		"- - hostname\n      - set\n      - example.com",
		"- - header\n      - set\n      - Host\n      - example.com",
		"- - header\n      - replace\n      - host\n      - example.com\n      - example.com",
		"should-remain.private.example",
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
		"origin_password: '*****'",
		"cache-control: no-cache",
		"EXPIRES: Thu, 01 Jan 1970 00:00:00 GMT",
		"Cache-Control: max-age=60",
		"expires: Fri, 02 Jan 1970 00:00:00 GMT",
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
		"redis-cache",
		"private-listener",
		"redis.private.example",
		"redis-a.private.example",
		"redis-b.private.example",
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
		"internal.private.example",
		"old.private.example",
		"new.private.example",
		"hostname.private.example",
		"header.private.example",
		"old-header.private.example",
		"new-header.private.example",
		"prom-a.private.example",
		"prom-b.private.example",
		"private-org",
		"private-env",
		"private-ha-shard",
		"graphite-main",
		"graphite.private.example",
		"graphite-secret",
		"graphite-probe-secret",
		"graphite-path-secret",
		"prom-append-secret",
	} {
		if strings.Contains(out, privateValue) {
			t.Errorf("expected sanitized config not to contain %q; got:\n%s", privateValue, out)
		}
	}

	if conf.Backends["prom-a"].CacheName != "cache-a" {
		t.Errorf("expected original backend cache name to remain unchanged")
	}
	if conf.Backends["prom-a"].ListenerName != "private-listener" {
		t.Errorf("expected original backend listener reference to remain unchanged")
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
	if conf.Backends["graphite-main"].Graphite.OriginPassword != "graphite-secret" {
		t.Errorf("expected original graphite origin credential to remain unchanged")
	}
	if conf.Backends["prom-a"].TracingConfigName != "traces-b" {
		t.Errorf("expected original backend tracing reference to remain unchanged")
	}
	if conf.TracingOptions["traces-b"].Endpoint != "traces-b.private.example:4317" {
		t.Errorf("expected original tracing endpoint to remain unchanged")
	}
	if conf.Caches["redis-cache"].Redis.Endpoint != "redis.private.example:6379" {
		t.Errorf("expected original redis endpoint to remain unchanged")
	}
	if conf.RequestRewriters["host-rewriter"].Instructions[0][2] != "internal.private.example:9090" {
		t.Errorf("expected original host rewriter to remain unchanged")
	}
	if conf.Backends["alb-users"].ALBOptions.UserRouter.Users["user-a"].ToBackend != "prom-b" {
		t.Errorf("expected original user router backend reference to remain unchanged")
	}
	if conf.Rules["route-rule"].CaseOptions[0].NextRoute != "prom-b" {
		t.Errorf("expected original rule case backend reference to remain unchanged")
	}
}

func TestSanitizedCloneKubernetesReferences(t *testing.T) {
	const yml = `
caches:
  edge-cache:
    provider: memory%s
negative_caches:
  misses:
    "404": 5s
authenticators:
  staff:
    provider: basic
tracing:
  private-traces:
    provider: stdout
listeners:
  ingress-listener:
    port: 9000
backends:
  web:
    provider: reverseproxycache
    origin_url: http://web.private.example
    cache_name: edge-cache
    negative_cache_name: misses
geo_locators:
  default:
    provider: geofeed
geo_acls:
  customer-region:
    allow: [US]
kubernetes:
  ingress:
    listener_names: [ingress-listener]
  defaults:
    routing_mode: service
    cache_name: edge-cache
    negative_cache_name: misses
    tracing_name: private-traces
    authenticator_name: staff
    geo_acl_name: customer-region
`
	// negative caches keep their names, even when an ordinary cache, which is renamed, shares one
	for name, sameNamedCache := range map[string]string{
		"own name":            "",
		"shared with a cache": "\n  misses:\n    provider: memory",
	} {
		t.Run(name, func(t *testing.T) {
			conf := NewConfig()
			if err := conf.loadYAMLConfig(fmt.Sprintf(yml, sameNamedCache)); err != nil {
				t.Fatal(err)
			}
			delete(conf.Backends, "default") // seeded by NewConfig, naming neither cache

			sanitized := conf.SanitizedClone()
			d := sanitized.Kubernetes.Defaults
			for field, ok := range map[string]bool{
				"cache_name":          hasKey(sanitized.Caches, d.CacheName),
				"negative_cache_name": hasKey(sanitized.NegativeCacheConfigs, d.NegativeCacheName),
				"tracing_name":        hasKey(sanitized.TracingOptions, d.TracingName),
				"authenticator_name":  hasKey(sanitized.Authenticators, d.AuthenticatorName),
				"geo_acl_name":        hasKey(sanitized.GeoACLs, d.GeoACLName),
				"ingress listener":    hasKey(sanitized.Listeners, sanitized.Kubernetes.Ingress.ListenerNames[0]),
			} {
				if !ok {
					t.Errorf("the kubernetes %s names no sanitized definition in its own section", field)
				}
			}
			if d.NegativeCacheName != "misses" {
				t.Errorf("expected the negative cache to keep its name, got %q", d.NegativeCacheName)
			}
			if d.GeoACLName != "geo-acl-1" {
				t.Errorf("expected the default geo ACL to be renamed, got %q", d.GeoACLName)
			}
			for _, b := range sanitized.Backends {
				if b.NegativeCacheName != d.NegativeCacheName || b.CacheName != d.CacheName {
					t.Errorf("backend caches %q and %q differ from the kubernetes defaults' %q and %q",
						b.CacheName, b.NegativeCacheName, d.CacheName, d.NegativeCacheName)
				}
			}

			out := conf.SanitizedString()
			for _, privateValue := range []string{
				"edge-cache", "private-traces", "staff", "customer-region", "ingress-listener",
			} {
				if strings.Contains(out, privateValue) {
					t.Errorf("expected sanitized config not to contain %q; got:\n%s", privateValue, out)
				}
			}

			if d := conf.Kubernetes.Defaults; d.GeoACLName != "customer-region" || d.CacheName != "edge-cache" ||
				d.NegativeCacheName != "misses" {
				t.Errorf("expected original kubernetes defaults to remain unchanged")
			}
			if conf.Kubernetes.Ingress.ListenerNames[0] != "ingress-listener" {
				t.Errorf("expected original ingress listener names to remain unchanged")
			}
		})
	}
}

func TestSanitizedCloneIPACLs(t *testing.T) {
	conf := NewConfig()
	err := conf.loadYAMLConfig(`
ip_acls:
  office-network:
    allow: [10.20.30.0/24, 198.51.100.7]
    deny: [all]
    allow_file: /etc/trickster/office.lst
  legacy-order:
    match: ordered
    rules:
      - deny: 10.20.30.40
      - allow: all
listeners:
  edge:
    port: 9000
    ip_acl_name: office-network
backends:
  web:
    provider: reverseproxycache
    origin_url: http://web.private.example
    listener_names: [edge]
    ip_acl_name: office-network
    paths:
      - path: /legacy/
        ip_acl_name: legacy-order
      - path: /open/
        ip_acl_name: none
kubernetes:
  defaults:
    routing_mode: service
    ip_acl_name: office-network
`)
	if err != nil {
		t.Fatal(err)
	}
	delete(conf.Backends, "default") // seeded by NewConfig, naming no list

	sanitized := conf.SanitizedClone()
	var listener, backend, legacy, open, defaults string
	for _, lo := range sanitized.Listeners {
		if lo.IPACLName != "" {
			listener = lo.IPACLName
		}
	}
	for _, b := range sanitized.Backends {
		backend = b.IPACLName
		for _, p := range b.Paths {
			switch p.Path {
			case "/legacy/":
				legacy = p.IPACLName
			case "/open/":
				open = p.IPACLName
			}
		}
	}
	defaults = sanitized.Kubernetes.Defaults.IPACLName
	for where, name := range map[string]string{
		"listener": listener, "backend": backend, "path": legacy, "kubernetes defaults": defaults,
	} {
		if !hasKey(sanitized.IPACLs, name) || !strings.HasPrefix(name, ipACLPrefix+"-") {
			t.Errorf("the %s names %q, which is no sanitized ip acl", where, name)
		}
	}
	if listener != backend || backend != defaults || backend == legacy {
		t.Errorf("references to one list were renamed apart: %q %q %q %q", listener, backend, defaults, legacy)
	}
	if open != "none" {
		t.Errorf("a path clearing its backend's list says %q", open)
	}
	office := sanitized.IPACLs[backend]
	if office.Name != backend || office.Allow[0] != sanitizedValue || office.Deny[0] != "all" ||
		office.AllowFile != "/etc/trickster/office.lst" {
		t.Errorf("unexpected sanitized list: %+v", office)
	}
	if rules := sanitized.IPACLs[legacy].Rules; rules[0].Deny != sanitizedValue || rules[1].Allow != "all" {
		t.Errorf("unexpected sanitized rules: %+v", rules)
	}

	out := conf.SanitizedString()
	for _, privateValue := range []string{"office-network", "legacy-order", "10.20.30", "198.51.100.7"} {
		if strings.Contains(out, privateValue) {
			t.Errorf("expected sanitized config not to contain %q; got:\n%s", privateValue, out)
		}
	}
	if conf.IPACLs["office-network"].Allow[0] != "10.20.30.0/24" || conf.Listeners["edge"].IPACLName != "office-network" ||
		conf.Kubernetes.Defaults.IPACLName != "office-network" {
		t.Errorf("expected the original ip acls and references to remain unchanged")
	}
}

func hasKey[V any](m map[string]V, k string) bool {
	_, ok := m[k]
	return ok
}

func TestConfigStringsRedactDSNAndAuthenticatorPasswords(t *testing.T) {
	const configSecretKey, configSecret = "client_secret", "config-super-secret"
	conf := NewConfig()
	err := conf.loadYAMLConfig(`
authenticators:
  mysql-clients:
    provider: basic
    users:
      grafana: authenticator-super-secret
    config:
      realm: visible-realm
      ` + configSecretKey + `: ` + configSecret + `
      oidc:
        signing_key: nested-super-secret
backends:
  mysql:
    provider: mysql
    origin_url: mysql://origin:dsn-super-secret@mysql.internal.example/database
    authenticator_name: mysql-clients
`)
	if err != nil {
		t.Fatal(err)
	}

	for name, output := range map[string]string{
		"String":          conf.String(),
		"SanitizedString": conf.SanitizedString(),
	} {
		for _, secret := range []string{
			"dsn-super-secret", "authenticator-super-secret",
			configSecret, "nested-super-secret",
		} {
			if strings.Contains(output, secret) {
				t.Errorf("%s exposed %q:\n%s", name, secret, output)
			}
		}
		for _, visible := range []string{"visible-realm", configSecretKey, "signing_key"} {
			if !strings.Contains(output, visible) {
				t.Errorf("%s hid %q:\n%s", name, visible, output)
			}
		}
	}
	if conf.Authenticators["mysql-clients"].ProviderData[configSecretKey] != configSecret {
		t.Error("the config views mutated the running authenticator config")
	}
}

func TestSanitizedCloneEdgeCases(t *testing.T) {
	conf := NewConfig()
	err := conf.loadYAMLConfig(`
caches:
  empty-provider:
    provider: ""
  unknown-cache:
    provider: custom-cache
tracing:
  empty-provider:
    provider: ""
  unknown-tracer:
    provider: custom-tracer
backends:
  empty-provider:
    provider: ""
    origin_url: http://example.com
    paths:
      - path: /ok
        authenticator_name: missing-auth
  unknown-backend:
    provider: custom-backend
    origin_url: http://example.com
    alb:
      pool:
        - missing-backend
      user_router:
        default_backend: missing-backend
        users:
          u1:
            to_backend: missing-backend
rules:
  route-rule:
    next_route: missing-backend
    cases:
      - matches: [a]
        next_route: missing-backend
request_rewriters:
  short:
    instructions:
      - [host]
      - [host, set, keep.example]
      - [header, set, X-Other, keep.example]
`)
	if err != nil {
		t.Fatalf("Could not load configuration: %s", err.Error())
	}

	out := conf.SanitizedString()
	for _, want := range []string{
		"cache-1:",
		"custom-cache-1:",
		"tracing-1:",
		"custom-tracer-1:",
		"backend-1:",
		"custom-backend-1:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected sanitized config to contain %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "empty-provider") || strings.Contains(out, "unknown-cache") {
		t.Errorf("expected empty/unknown names to be anonymized; got:\n%s", out)
	}
}

func TestSanitizedStringACME(t *testing.T) {
	conf := NewConfig()
	err := conf.loadYAMLConfig(`
listeners:
  edge-private:
    port: 9480
    tls_port: 9483
caches:
  acme-redis:
    provider: redis
acme:
  storage:
    provider: redis
    redis:
      connection:
        endpoint: redis.private.example:6379
        password: hunter2
  issuers:
    le:
      email: ops@private.example
      agree_to_terms: true
      dns_provider:
        provider: rfc2136
        rfc2136:
          server: ns.private.example:53
    r53:
      agree_to_terms: true
      dns_provider:
        provider: route53
        route53:
          hosted_zone_id: ZPRIVATE
  on_demand:
    issuer: le
    listeners: [edge-private]
    ask: https://ask.private.example/check
`)
	if err != nil {
		t.Fatal(err)
	}
	out := conf.SanitizedString()
	for _, private := range []string{
		"redis.private.example", "hunter2", "ops@private.example",
		"ns.private.example", "ZPRIVATE", "ask.private.example", "edge-private",
	} {
		if strings.Contains(out, private) {
			t.Errorf("sanitized output contains %q", private)
		}
	}
	if strings.Contains(conf.String(), "hunter2") {
		t.Error("the config dump contains the ACME Redis password")
	}
	conf.ACME.Storage.Redis = &acmeopts.RedisStorageOptions{CacheName: "acme-redis"}
	if out := conf.SanitizedString(); strings.Contains(out, "cache_name: acme-redis") {
		t.Error("the borrowed cache name was not anonymized")
	}
	sanitizeACME(nil, nil, nil)
}
