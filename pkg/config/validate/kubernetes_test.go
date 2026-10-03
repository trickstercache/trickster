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
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	cacheopts "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	geofeedopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/options"
	headeropts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/header/options"
	geolocopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ipacl"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"

	"github.com/stretchr/testify/require"
)

// baseConfig is a config that validates, so a kubernetes test breaks only
// what it means to
func baseConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]string{
		"-origin-url", "http://example.com",
		"-provider", "rp",
	})
	require.NoError(t, err)
	require.NoError(t, Validate(c))
	return c
}

// A misconfigured controller must fail startup with the rest of the
// configuration, not at the first watch event
func TestValidateKubernetesSection(t *testing.T) {
	c := baseConfig(t)
	c.Kubernetes = kubecfg.New()
	err := Validate(c)
	require.ErrorIs(t, err, kubecfg.ErrRoutingModeRequired)
	require.ErrorContains(t, err, "kubernetes:",
		"the failure must name the section it came from")

	c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
	require.NoError(t, Validate(c))

	c.Kubernetes.Defaults.RoutingMode = "guess"
	require.ErrorIs(t, Validate(c), kubecfg.ErrInvalidRoutingMode)
}

// The section is optional; its absence is not a validation failure
func TestValidateWithoutKubernetesSection(t *testing.T) {
	c := baseConfig(t)
	require.Nil(t, c.Kubernetes)
	require.NoError(t, Validate(c))
}

// The listeners claimed Ingresses are served on are checked the same way a
// backend's are: the controller generates backends that name them, so an
// undefined one would fail every reload the controller asks for rather
// than this one
func TestValidateKubernetesListeners(t *testing.T) {
	c := baseConfig(t)
	c.Kubernetes = kubecfg.New()
	c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
	require.NoError(t, Validate(c), "the default frontend is always defined")

	c.Kubernetes.Ingress = &kubecfg.IngressOptions{
		ListenerNames: []string{"undefined"},
	}
	require.ErrorContains(t, Validate(c),
		`kubernetes 'ingress' references undefined listener "undefined"`)

	c.Listeners["undefined"] = listener.New("undefined")
	require.NoError(t, Validate(c))

	// a disabled section is not checked, so an operator turning the
	// controller off does not have to keep the rest of the block valid
	delete(c.Listeners, "undefined")
	c.Kubernetes.Enabled = new(false)
	require.NoError(t, Validate(c))
}

// Every object the section names is checked against the configuration that
// defines it. These are the operator's own references, so an undefined one
// fails startup rather than the first reconcile, where it would look like
// the controller having stopped working rather than like a typo.
func TestValidateKubernetesDefaultsReferences(t *testing.T) {
	tests := []struct {
		name, kind string
		set        func(*kubecfg.DefaultsOptions)
		define     func(*config.Config)
	}{
		{
			name: "cache", kind: "cache",
			set:    func(d *kubecfg.DefaultsOptions) { d.CacheName = "absent" },
			define: func(c *config.Config) { c.Caches["absent"] = cacheopts.New() },
		},
		{
			name: "negative cache", kind: "negative cache",
			set: func(d *kubecfg.DefaultsOptions) {
				d.NegativeCacheName = "absent"
			},
			define: func(c *config.Config) {
				c.NegativeCacheConfigs["absent"] = negative.Config{"404": time.Minute}
			},
		},
		{
			name: "tracing", kind: "tracing config",
			set:    func(d *kubecfg.DefaultsOptions) { d.TracingName = "absent" },
			define: func(c *config.Config) { c.TracingOptions["absent"] = to.New() },
		},
		{
			name: "rewriter", kind: "request rewriter",
			set: func(d *kubecfg.DefaultsOptions) {
				d.ReqRewriterName = "absent"
			},
			define: func(c *config.Config) {
				if c.RequestRewriters == nil {
					c.RequestRewriters = rwopts.Lookup{}
				}
				c.RequestRewriters["absent"] = rwopts.New()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := baseConfig(t)
			c.Kubernetes = kubecfg.New()
			c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
			test.set(c.Kubernetes.Defaults)
			require.ErrorContains(t, Validate(c),
				`kubernetes 'defaults' references undefined `+
					test.kind+` "absent"`)

			test.define(c)
			require.NoError(t, Validate(c))
		})
	}
}

// An authenticator has no annotation, so naming one is always the
// operator's, and is checked the same way
func TestValidateKubernetesAuthenticatorReference(t *testing.T) {
	c := baseConfig(t)
	c.Kubernetes = kubecfg.New()
	c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
	c.Kubernetes.Defaults.AuthenticatorName = "absent"
	require.ErrorContains(t, Validate(c),
		`kubernetes 'defaults' references undefined authenticator "absent"`)
}

func TestValidateKubernetesGeoACLReference(t *testing.T) {
	// a geo ACL in the defaults gates every generated route, stream routes included, so its locator must
	// place a bare address
	c := baseConfig(t)
	c.Kubernetes = kubecfg.New()
	c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
	c.Kubernetes.Defaults.GeoACLName = "absent"
	require.ErrorContains(t, Validate(c), `kubernetes 'defaults' references undefined geo ACL "absent"`)

	geoACL(c, geoACLEdge, geoLocatorEdge, nil)
	c.Kubernetes.Defaults.GeoACLName = geoACLEdge
	c.GeoLocators[geoLocatorEdge].Header = &headeropts.Options{Country: "CF-IPCountry"}
	require.ErrorContains(t, Validate(c), "judges HTTP requests only")

	geoACL(c, geoACLNorthAmerica, geolocopts.DefaultName, nil)
	c.GeoLocators[geolocopts.DefaultName].Geofeed = &geofeedopts.Options{Entries: []string{"192.0.2.0/24,US"}}
	c.Kubernetes.Defaults.GeoACLName = geoACLNorthAmerica
	require.NoError(t, Validate(c))
}

// An access list named by the defaults is the operator's, checked the same way
// as an authenticator. A list that exists but is peer or drop is a different
// failure: the name is defined, and a generated backend still cannot use it.
func TestValidateKubernetesIPACLReference(t *testing.T) {
	with := func(t *testing.T, name string, opts ipacl.Options) *config.Config {
		t.Helper()
		c := baseConfig(t)
		c.Kubernetes = kubecfg.New()
		c.Kubernetes.Defaults.RoutingMode = kubecfg.RoutingModeService
		c.Kubernetes.Defaults.IPACLName = name
		if opts.Allow != nil || opts.Source != "" || opts.Action != "" {
			c.IPACLs = ipacl.Lookup{name: &opts}
		}
		return c
	}

	t.Run("eligible", func(t *testing.T) {
		c := with(t, "office", ipacl.Options{Allow: []string{"10.0.0.0/8"}})
		require.NoError(t, Validate(c))
	})

	t.Run("undefined", func(t *testing.T) {
		c := with(t, "absent", ipacl.Options{})
		require.ErrorContains(t, Validate(c),
			`kubernetes 'defaults' references undefined ip acl "absent"`)
	})

	t.Run("peer", func(t *testing.T) {
		c := with(t, "edge", ipacl.Options{Allow: []string{"10.0.0.0/8"}, Source: "peer"})
		err := Validate(c)
		require.ErrorContains(t, err, `ineligible ip acl "edge"`)
		require.NotContains(t, err.Error(), "undefined")
	})

	t.Run("drop", func(t *testing.T) {
		c := with(t, "wall", ipacl.Options{Allow: []string{"10.0.0.0/8"}, Action: "drop"})
		err := Validate(c)
		require.ErrorContains(t, err, `ineligible ip acl "wall"`)
		require.NotContains(t, err.Error(), "undefined")
	})
}
