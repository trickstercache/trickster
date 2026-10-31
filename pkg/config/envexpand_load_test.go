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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"github.com/stretchr/testify/require"
)

const envExpansionConfig = `
frontend:
  listen_port: ${TRK_TEST_PORT}
caches:
  shared:
    provider: redis
    redis:
      endpoint: ${TRK_TEST_REDIS_HOST:-127.0.0.1}:6379
      password: "${TRK_TEST_REDIS_PW}"
authenticators:
  clients:
    provider: basic
    users:
      alice: "${TRK_TEST_USER_PW}"
      bob: "$2y$10$literal.bcrypt.hash"
      carol: "$${TRK_TEST_USER_PW}"
discovery:
  registry:
    provider: consul
    http:
      endpoint: http://127.0.0.1:8500
      username: reader
      password: ${TRK_TEST_DISCOVERY_PW}
      headers:
        X-Consul-Token: "${TRK_TEST_DISCOVERY_TOKEN}"
  jobs:
    provider: nomad
    http:
      endpoint: http://127.0.0.1:4646
      bearer_token: ${TRK_TEST_DISCOVERY_BEARER}
tracing:
  otel:
    provider: otlp
    endpoint: http://127.0.0.1:4318
    headers:
      X-Honeycomb-Team: ${TRK_TEST_TRACING_KEY}
request_rewriters:
  to-origin:
    instructions:
      - [ 'hostname', 'set', '${site}.${TRK_TEST_DOMAIN}' ]
backends:
  origin:
    provider: reverseproxycache
    origin_url: http://${TRK_TEST_ORIGIN_HOST}:8080
    cache_name: shared
    timeout: ${TRK_TEST_UNSET_TIMEOUT}
    max_ttl: ${TRK_TEST_MAX_TTL:-90s}
    healthcheck:
      headers:
        X-Health-Token: "${TRK_TEST_HEALTH_TOKEN}"
    cors:
      headers:
        Access-Control-Allow-Origin: "${TRK_TEST_CORS_ORIGIN}"
    paths:
      - path: /
        match_type: prefix
        handler: proxycache
        cors:
          headers:
            Access-Control-Allow-Origin: "${TRK_TEST_PATH_CORS_ORIGIN}"
        request_headers:
          X-Auth-Token: "${TRK_TEST_REQUEST_HEADER}"
        request_params:
          token: "${TRK_TEST_REQUEST_PARAM}"
        response_headers:
          X-Served-By: "${TRK_TEST_RESPONSE_HEADER}"
  graphite:
    provider: graphite
    origin_url: http://127.0.0.1:8081
    graphite:
      origin_username: reader
      origin_password: ${TRK_TEST_GRAPHITE_PW}
  router:
    provider: alb
    authenticator_name: clients
    alb:
      mechanism: ur
      user_router:
        users:
          alice:
            to_backend: origin
            to_user: reader
            to_credential: "${TRK_TEST_TO_CREDENTIAL}"
`

var envExpansionSecrets = map[string]string{
	"TRK_TEST_REDIS_PW":         "redis-pw",
	"TRK_TEST_USER_PW":          "user-pw",
	"TRK_TEST_DISCOVERY_PW":     "discovery-pw",
	"TRK_TEST_DISCOVERY_TOKEN":  "discovery-token",
	"TRK_TEST_DISCOVERY_BEARER": "discovery-bearer",
	"TRK_TEST_TRACING_KEY":      "tracing-key",
	"TRK_TEST_HEALTH_TOKEN":     "health-token",
	"TRK_TEST_REQUEST_HEADER":   "request-header",
	"TRK_TEST_REQUEST_PARAM":    "request-param",
	"TRK_TEST_GRAPHITE_PW":      "graphite-pw",
	// an expanded value is never expanded again
	"TRK_TEST_TO_CREDENTIAL": "cred-${TRK_TEST_USER_PW}",
}

func loadEnvExpansionConfig(t *testing.T) *Config {
	t.Helper()
	for k, v := range envExpansionSecrets {
		t.Setenv(k, v)
	}
	for k, v := range map[string]string{
		"TRK_TEST_PORT":             "18480",
		"TRK_TEST_DOMAIN":           "origins.example.com",
		"TRK_TEST_ORIGIN_HOST":      "origin.internal",
		"TRK_TEST_CORS_ORIGIN":      "https://backend.example",
		"TRK_TEST_PATH_CORS_ORIGIN": "https://path.example",
		"TRK_TEST_RESPONSE_HEADER":  "response-header",
		// a capture token's name, set in the environment, must not reach a rewriter
		"site": "leaked",
	} {
		t.Setenv(k, v)
	}
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(envExpansionConfig), 0o600))
	conf, err := Load([]string{"-config", path})
	require.NoError(t, err)
	return conf
}

func TestLoadExpandsEnvReferences(t *testing.T) {
	conf := loadEnvExpansionConfig(t)

	require.Equal(t, 18480, conf.Frontend.ListenPort)
	redis := conf.Caches["shared"].Redis
	require.Equal(t, "127.0.0.1:6379", redis.Endpoint)
	require.Equal(t, "redis-pw", string(redis.Password))

	users := conf.Authenticators["clients"].Users
	require.Equal(t, "user-pw", users["alice"])
	require.Equal(t, "$2y$10$literal.bcrypt.hash", users["bob"])
	require.Equal(t, "${TRK_TEST_USER_PW}", users["carol"])

	registry := conf.Discovery["registry"].HTTP
	require.Equal(t, "discovery-pw", string(registry.Password))
	require.Equal(t, "discovery-token", registry.Headers["X-Consul-Token"])
	require.Equal(t, "discovery-bearer", string(conf.Discovery["jobs"].HTTP.BearerToken))
	require.Equal(t, "tracing-key", conf.TracingOptions["otel"].Headers["X-Honeycomb-Team"])

	require.Equal(t, [][]string{{"hostname", "set", "${site}.origins.example.com"}},
		[][]string(conf.RequestRewriters["to-origin"].Instructions))

	origin := conf.Backends["origin"]
	require.Equal(t, "http://origin.internal:8080", origin.OriginURL)
	require.Equal(t, time.Minute, time.Duration(origin.Timeout), "an unset reference keeps the default")
	require.Equal(t, 90*time.Second, time.Duration(origin.MaxTTL))
	require.Equal(t, "health-token", origin.HealthCheck.Headers["X-Health-Token"])
	require.Equal(t, "https://backend.example",
		origin.CORS.Headers["Access-Control-Allow-Origin"])
	var root *po.Options
	for _, p := range origin.Paths {
		if p.Path == "/" && len(p.RequestHeaders) > 0 {
			root = p
		}
	}
	require.NotNil(t, root)
	require.Equal(t, "https://path.example", root.CORS.Headers["Access-Control-Allow-Origin"])
	require.Equal(t, "request-header", root.RequestHeaders["X-Auth-Token"])
	require.Equal(t, "request-param", root.RequestParams["token"])
	require.Equal(t, "response-header", root.ResponseHeaders["X-Served-By"])

	require.Equal(t, "graphite-pw", string(conf.Backends["graphite"].Graphite.OriginPassword))
	alice := conf.Backends["router"].ALBOptions.UserRouter.Users["alice"]
	require.Equal(t, "cred-${TRK_TEST_USER_PW}", string(alice.ToCredential))

	require.Contains(t, conf.LoaderWarnings,
		"config references environment variable TRK_TEST_UNSET_TIMEOUT, which is not set")
}

func TestConfigStringRedactsExpandedSecrets(t *testing.T) {
	conf := loadEnvExpansionConfig(t)
	s := conf.String()
	for name, secretValue := range envExpansionSecrets {
		require.NotContains(t, s, secretValue, name)
	}
	// the view redacts a clone, so the loaded values are untouched
	require.Equal(t, "discovery-token", conf.Discovery["registry"].HTTP.Headers["X-Consul-Token"])
	require.Equal(t, "tracing-key", conf.TracingOptions["otel"].Headers["X-Honeycomb-Team"])
}

func TestLoadRequiredEnvReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`backends:
  default:
    provider: rpc
    origin_url: ${TRK_TEST_REQUIRED_URL:?set the origin}
`), 0o600))
	_, err := Load([]string{"-config", path})
	require.ErrorContains(t, err, path)
	require.ErrorContains(t, err, "environment variable TRK_TEST_REQUIRED_URL is required: set the origin")
}

func TestLoadExpandsEachSourceOnce(t *testing.T) {
	// with an include directory the sources merge, then decode from the merged text
	dir := t.TempDir()
	include := filepath.Join(dir, "fragments")
	require.NoError(t, os.Mkdir(include, 0o700))
	t.Setenv("TRK_TEST_INCLUDE", "fragments")
	t.Setenv("TRK_TEST_ORIGIN", "http://127.0.0.1:9090")
	path := filepath.Join(dir, "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`main:
  config_include_directory: ${TRK_TEST_INCLUDE}
backends:
  default:
    provider: rpc
    origin_url: ${TRK_TEST_ORIGIN}
`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(include, "paths.yaml"), []byte(`backends:
  default:
    paths:
      - path: /
        match_type: prefix
        handler: proxy
        request_headers:
          X-Literal: "$${TRK_TEST_ORIGIN}"
`), 0o600))
	conf, err := Load([]string{"-config", path})
	require.NoError(t, err)
	d := conf.Backends["default"]
	require.Equal(t, "http://127.0.0.1:9090", d.OriginURL)
	var literal string
	for _, p := range d.Paths {
		if v, ok := p.RequestHeaders["X-Literal"]; ok {
			literal = v
		}
	}
	require.Equal(t, "${TRK_TEST_ORIGIN}", literal, "the fragment was included and expanded once")
}

func TestLoadKeepsSourceTextWithoutReferences(t *testing.T) {
	// decode errors cite the source's own lines when nothing was expanded
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte("frontend:\n\n\n  listen_port: nope\n"), 0o600))
	_, err := Load([]string{"-config", path})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "line 4"), err.Error())
}

func TestLoadRequiredEnvReferenceInSources(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.yaml"),
		[]byte("backends:\n  default:\n    provider: rpc\n    origin_url: http://127.0.0.1:9090\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.yaml"),
		[]byte("backends:\n  default:\n    timeout: ${TRK_TEST_NEEDED:?}\n"), 0o600))
	_, err := Load([]string{"-config", dir})
	require.ErrorContains(t, err, "environment variable TRK_TEST_NEEDED is required")
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("main:\n  config_include_directory: ${TRK_TEST_NEEDED:?}\n"), 0o600))
	_, err = Load([]string{"-config", path})
	require.ErrorContains(t, err, "environment variable TRK_TEST_NEEDED is required")
}
