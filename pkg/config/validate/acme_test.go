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

package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"

	"github.com/stretchr/testify/require"
)

const acmeBaseYAML = `
listeners:
  default:
    port: 8480
    tls_port: 8483
caches:
  shared:
    provider: redis
  mem:
    provider: memory
acme:
  storage:
    path: /var/lib/trickster/acme
  issuers:
    le:
      agree_to_terms: true
    dns:
      agree_to_terms: true
      dns_provider:
        provider: route53
backends:
  site:
    provider: rp
    origin_url: http://127.0.0.1:9090
    cache_name: mem
    hosts: [WWW.acme.test, api.acme.test]
    tls:
      acme:
        issuer: le
`

func loadACMEConfig(t *testing.T, replacements ...string) (*config.Config, error) {
	t.Helper()
	doc := acmeBaseYAML
	for i := 0; i+1 < len(replacements); i += 2 {
		require.Contains(t, doc, replacements[i])
		doc = strings.Replace(doc, replacements[i], replacements[i+1], 1)
	}
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(doc), 0o600))
	c, err := config.Load([]string{"-config", path})
	require.NoError(t, err)
	return c, Validate(c)
}

func TestValidateACME(t *testing.T) {
	c, err := loadACMEConfig(t)
	require.NoError(t, err)
	require.Equal(t, []string{"api.acme.test", "www.acme.test"},
		c.Backends["site"].TLS.ACME.ResolvedDomains)
	require.True(t, c.Listeners["default"].ServeTLS)
	uses, http01 := c.ListenerACME("default")
	require.True(t, uses)
	require.True(t, http01)
}

func TestValidateACMEErrors(t *testing.T) {
	tests := map[string]struct {
		replacements []string
		want         string
	}{
		"section invalid": {
			[]string{"agree_to_terms: true\n    dns:", "agree_to_terms: false\n    dns:"},
			"agree_to_terms must be true",
		},
		"undefined issuer": {[]string{"issuer: le", "issuer: missing"}, "undefined acme issuer"},
		"no domains":       {[]string{"hosts: [WWW.acme.test, api.acme.test]", "hosts: []"}, "tls.acme needs domains"},
		"wildcard http-01": {
			[]string{"hosts: [WWW.acme.test, api.acme.test]", `hosts: ["*.acme.test"]`},
			"requires an issuer using dns-01",
		},
		"cache undefined": {[]string{
			"path: /var/lib/trickster/acme",
			"provider: redis\n    redis:\n      cache_name: none",
		}, "references undefined cache"},
		"cache not redis": {[]string{
			"path: /var/lib/trickster/acme",
			"provider: redis\n    redis:\n      cache_name: mem",
		}, "is not a redis cache"},
		"no tls port": {[]string{"tls_port: 8483", "tls_port: 0"}, "an http listener with a tls_port"},
		"on demand nowhere": {
			[]string{
				"backends:",
				"  on_demand:\n    issuer: le\n    listeners: [nope]\n    ask: http://a\nbackends:",
			},
			"references undefined listener",
		},
		"no issuers": {[]string{"  issuers:\n    le:\n      agree_to_terms: true\n    dns:\n      agree_to_terms: true\n" +
			"      dns_provider:\n        provider: route53\n", ""}, "must declare at least one issuer"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadACMEConfig(t, test.replacements...)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestValidateACMEDomainOwnership(t *testing.T) {
	_, err := loadACMEConfig(t, "backends:", `backends:
  wild:
    provider: rp
    origin_url: http://127.0.0.1:9091
    cache_name: mem
    hosts: [api.acme.test]
    tls:
      acme:
        issuer: dns`)
	require.ErrorContains(t, err, `acme domain "api.acme.test" is already issued by`)

	c, err := loadACMEConfig(t, "backends:", `backends:
  wild:
    provider: rp
    origin_url: http://127.0.0.1:9091
    cache_name: mem
    hosts: ["*.wild.acme.test"]
    tls:
      acme:
        issuer: dns`)
	require.NoError(t, err)
	require.Equal(t, []string{"*.wild.acme.test"}, c.Backends["wild"].TLS.ACME.ResolvedDomains)
}

func TestValidateACMEWithoutSection(t *testing.T) {
	_, err := loadACMEConfig(t, "acme:\n  storage:\n    path: /var/lib/trickster/acme\n", "nothing:\n  storage:\n    path: x\n")
	require.ErrorContains(t, err, "the acme section declares no issuers")
}

func TestValidateACMEWithKubernetes(t *testing.T) {
	c, err := loadACMEConfig(t)
	require.NoError(t, err)
	c.Kubernetes = kubecfg.New()
	c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
	require.ErrorIs(t, Validate(c), ErrACMEWithKubernetes)
}

func TestValidateACMEWarnsWithoutPlaintextPort(t *testing.T) {
	c, err := loadACMEConfig(t, "port: 8480\n", "port: 0\n")
	require.NoError(t, err)
	require.Contains(t, strings.Join(c.LoaderWarnings, "\n"), "http-01 challenges")
}

func TestValidateACMERedisStorageKeepsBorrowedCache(t *testing.T) {
	c, err := loadACMEConfig(t, "path: /var/lib/trickster/acme",
		"provider: redis\n    redis:\n      cache_name: shared")
	require.NoError(t, err)
	require.Contains(t, c.Caches, "shared", "a cache only ACME storage uses must not be pruned")
}

func TestValidateACMEOnDemandListener(t *testing.T) {
	onDemand := "  on_demand:\n    issuer: le\n    listeners: [edge]\n    ask: http://127.0.0.1:9000/check\nbackends:"
	edge := "listeners:\n  edge:\n    port: 9480\n    tls_port: 9483\n  default:"
	c, err := loadACMEConfig(t, "backends:", onDemand, "listeners:\n  default:", edge,
		"backends:\n  site:", "backends:\n  plain:\n    provider: rp\n    origin_url: http://127.0.0.1:9092\n"+
			"    cache_name: mem\n    listeners: [edge]\n  site:")
	require.NoError(t, err)
	require.True(t, c.Listeners["edge"].ServeTLS, "an on-demand listener serves TLS without a mapped certificate")

	_, err = loadACMEConfig(t, "backends:", onDemand, "listeners:\n  default:",
		"listeners:\n  edge:\n    port: 9480\n  default:",
		"backends:\n  site:", "backends:\n  plain:\n    provider: rp\n    origin_url: http://127.0.0.1:9092\n"+
			"    cache_name: mem\n    listeners: [edge]\n  site:")
	require.ErrorContains(t, err, "must be an http listener with a tls_port")
}
