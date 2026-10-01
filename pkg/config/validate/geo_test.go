/*
 * Copyright 2026 The Trickster Authors
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
	"slices"
	"strings"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	bp "github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/config/listener"
	"github.com/trickstercache/trickster/v2/pkg/config/types"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	geolocopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"
	pathopts "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/pgwire/options"

	"github.com/stretchr/testify/require"
)

const (
	geoACLNorthAmerica = "north-america"
	geoACLEurope       = "europe"
	geoACLEdge         = "edge-countries"
	geoLocatorEdge     = "edge"
	geoBackendName     = "web"
)

const geoBaseConfig = `
backends:
  web:
    provider: reverseproxycache
    origin_url: http://127.0.0.1:1
    geo_acl_name: north-america
    paths:
      - path: /trailers/
        geo_acl_name: none
      - path: /eu/
        geo_acl_name: europe
geo_locators:
  default:
    provider: geofeed
    geofeed:
      entries: ["192.0.2.0/24,US"]
  edge:
    provider: HEADER
    header:
      country: CF-IPCountry
geo_acls:
  north-america:
    allow: [US, CA, MX]
    exempt: [private]
  europe:
    allow: [continent:EU]
    unknown: deny
  edge-countries:
    geo_locator_name: edge
    deny: [FR]
`

func loadGeoConfig(t *testing.T, yaml string) (*config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trickster.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	c, err := config.Load([]string{"-config", path})
	if err != nil {
		return nil, err
	}
	return c, Validate(c)
}

func TestGeoConfig(t *testing.T) {
	c, err := loadGeoConfig(t, geoBaseConfig)
	require.NoError(t, err)
	require.Equal(t, providers.Header, c.GeoLocators[geoLocatorEdge].Provider)
	web := c.Backends[geoBackendName]
	require.Same(t, c.GeoACLs[geoACLNorthAmerica], web.GeoACLOptions)
	var trailers, eu bool
	for _, p := range web.Paths {
		switch p.Path {
		case "/trailers/":
			trailers = true
			require.Nil(t, p.GeoACLOptions, "none clears the backend's geo ACL")
		case "/eu/":
			eu = true
			require.Same(t, c.GeoACLs[geoACLEurope], p.GeoACLOptions)
		}
	}
	require.True(t, trailers && eu)

	// validating again changes nothing and repeats no warning
	warnings := slices.Clone(c.LoaderWarnings)
	require.NoError(t, Validate(c))
	require.Equal(t, warnings, c.LoaderWarnings)
	require.Same(t, c.GeoACLs[geoACLNorthAmerica], web.GeoACLOptions)

	// the configuration endpoint shows both sections, sanitized
	s := c.String()
	require.Contains(t, s, "geo_locators:")
	require.Contains(t, s, "geo_acls:")
	require.Contains(t, s, "geo_acl_name: north-america")
	s = c.SanitizedString()
	require.Contains(t, s, "geo_acl_name: geo-acl-")
	require.NotContains(t, s, geoACLNorthAmerica)
	require.NotContains(t, s, "192.0.2.0/24")
	require.Contains(t, s, "geo_locator_name: geo-locator-1")
	require.Contains(t, s, "private")
}

func TestGeoConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		from, to string
		want     string
	}{
		"undefined geo acl":         {"geo_acl_name: north-america", "geo_acl_name: nowhere", `invalid geo_acl_name "nowhere"`},
		"none on a backend":         {"geo_acl_name: north-america", "geo_acl_name: none", `invalid geo_acl_name "none"`},
		"undefined path geo acl":    {"geo_acl_name: europe", "geo_acl_name: asia", `invalid geo_acl_name "asia"`},
		"undefined locator":         {"geo_locator_name: edge", "geo_locator_name: cdn", `undefined geo locator "cdn"`},
		"no default locator":        {"  default:\n    provider: geofeed", "  feed:\n    provider: geofeed", "no geo locator is named"},
		"invalid provider":          {"provider: HEADER", "provider: maxmind", `invalid provider "maxmind"`},
		"another provider's block":  {"    header:\n", "    mmdb: {}\n    header:\n", `"mmdb" options block`},
		"both lists":                {"deny: [FR]", "deny: [FR]\n    allow: [DE]", "exactly one of"},
		"unassigned country":        {"deny: [FR]", "deny: [UK]", "use GB"},
		"bad action":                {"unknown: deny", "unknown: deny\n    action: block", "invalid geo ACL action"},
		"bad unknown":               {"unknown: deny", "unknown: maybe", "invalid geo ACL unknown verdict"},
		"reserved locator name":     {"  edge:\n    provider: HEADER", "  none:\n    provider: HEADER", "invalid geo locator name"},
		"reserved geo acl name":     {"  europe:\n    allow", "  none:\n    allow", "invalid geo ACL name"},
		"bad geofeed entry":         {`entries: ["192.0.2.0/24,US"]`, `entries: ["bad"]`, "invalid geofeed line"},
		"unreadable geofeed file":   {`entries: ["192.0.2.0/24,US"]`, `files: [/nonexistent/feed.csv]`, "'files'"},
		"bad response status":       {"deny: [FR]", "deny: [FR]\n    response: {status: 302}", "'status' must be"},
		"message with a line break": {"deny: [FR]", "deny: [FR]\n    message: \"a\\nb\"", "invalid 'message'"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, geoBaseConfig, tc.from)
			_, err := loadGeoConfig(t, strings.Replace(geoBaseConfig, tc.from, tc.to, 1))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestGeoConfigWarnings(t *testing.T) {
	c, err := loadGeoConfig(t, strings.Replace(geoBaseConfig, "    exempt: [private]\n", "", 1)+`
  court-order:
    deny: [FR]
    response:
      status: 451
  lenient:
    allow: [US]
    unknown: allow
`)
	require.NoError(t, err)
	joined := strings.Join(c.LoaderWarnings, "\n")
	require.Contains(t, joined, `geo ACL "north-america" has an 'allow' list and no 'exempt' or 'unknown'`)
	require.Contains(t, joined, `geo ACL "court-order" answers 451`)
	require.Contains(t, joined, `geo ACL "lenient" allows every client`)
}

func geoACL(c *config.Config, name, locator string, edit func(*geoaclopts.Options)) {
	if c.GeoLocators == nil {
		c.GeoLocators = geolocopts.Lookup{}
		c.GeoACLs = geoaclopts.Lookup{}
	}
	provider := providers.Geofeed
	if locator == geoLocatorEdge {
		provider = providers.Header
	}
	c.GeoLocators[locator] = &geolocopts.Options{Name: locator, Provider: provider}
	o := &geoaclopts.Options{Name: name, GeoLocatorName: locator, Deny: []string{"FR"}}
	if edit != nil {
		edit(o)
	}
	c.GeoACLs[name] = o
}

func validateGeoListeners(c *config.Config) error {
	if err := c.Backends.ValidateGeoACLNames(c.GeoACLs); err != nil {
		return err
	}
	return Listeners(c)
}

func TestGeoListenerChecks(t *testing.T) {
	greptime := func(names ...string) *config.Config {
		c := config.NewConfig()
		c.Listeners["mysql"] = &listener.Options{Protocol: listener.ProtocolMySQL, ListenPort: 8490}
		c.Listeners["postgres"] = &listener.Options{Protocol: listener.ProtocolPostgres, ListenPort: 8491}
		o := bo.New()
		o.Provider, o.OriginURL, o.ListenerNames = "greptimedb", "http://db.example:4000", names
		o.MySQL = mo.New()
		o.MySQL.UpstreamURL = "mysql://reader:dev-password@db.example/public"
		o.Postgres = po.New()
		o.Postgres.UpstreamURL = "postgres://reader:dev-password@db.example/public"
		o.AuthenticatorName = "native-clients"
		o.AuthOptions = &autho.Options{Users: types.EnvStringMap{"client": "dev-password"}}
		c.Backends = bo.Lookup{"greptime": o}
		return c
	}
	validate := validateGeoListeners

	// one backend on an http, a mysql and a postgres listener is gated on all three by one geo ACL
	c := greptime("default", "mysql", "postgres")
	geoACL(c, geoACLNorthAmerica, geolocopts.DefaultName, nil)
	c.Backends["greptime"].GeoACLName = geoACLNorthAmerica
	require.NoError(t, validate(c))
	require.NoError(t, validate(c))

	// a header locator judges HTTP requests only
	c = greptime("default", "mysql", "postgres")
	geoACL(c, geoACLEdge, geoLocatorEdge, nil)
	c.Backends["greptime"].GeoACLName = geoACLEdge
	require.ErrorContains(t, validate(c), "judges HTTP requests only")
	c.Backends["greptime"].ListenerNames = []string{"default"}
	require.NoError(t, validate(c))
	require.Contains(t, strings.Join(c.LoaderWarnings, "\n"), "believes headers only from trusted_proxies")
	c.LoaderWarnings = nil
	c.Listeners["default"].TrustedProxies = []string{"10.0.0.0/8"}
	require.NoError(t, validate(c))
	require.NotContains(t, strings.Join(c.LoaderWarnings, "\n"), "trusted_proxies")

	// a backend-level geo ACL on a member that a native listener's alb routes to is refused, and a
	// path-level one, which judges the member's own HTTP requests, is not
	c = replicaConfig("rr")
	geoACL(c, geoACLNorthAmerica, geolocopts.DefaultName, nil)
	c.Backends["replica-a"].GeoACLName = geoACLNorthAmerica
	require.ErrorContains(t, validate(c), "native protocol listener's alb")
	c.Backends["replica-a"].GeoACLName = ""
	c.Backends["replicas"].GeoACLName = geoACLNorthAmerica
	require.NoError(t, validate(c))

	// a PROXY protocol header from any peer can name any source
	c = greptime("mysql")
	geoACL(c, geoACLNorthAmerica, geolocopts.DefaultName, nil)
	c.Backends["greptime"].GeoACLName = geoACLNorthAmerica
	c.Listeners["mysql"].ProxyProtocol = true
	require.NoError(t, validate(c))
	require.Contains(t, strings.Join(c.LoaderWarnings, "\n"), "takes the PROXY protocol from any peer")
}

func TestGeoPathOnRoutedMember(t *testing.T) {
	// a member that a mysql listener's alb balances sessions over also serves HTTP requests of its own
	c := replicaConfig("rr")
	for _, name := range []string{"replica-a", "replica-b"} {
		o := c.Backends[name]
		o.Provider, o.OriginURL = "greptimedb", "http://db.example:4000"
		o.MySQL = mo.New()
		o.MySQL.UpstreamURL = "mysql://reader:dev-password@db.example/public"
	}
	geoACL(c, geoACLEdge, geoLocatorEdge, nil)
	member := c.Backends["replica-a"]
	member.ListenerNames = []string{"default"}
	member.Paths = pathopts.List{{Path: "/api/", GeoACLName: geoACLEdge}}
	require.NoError(t, c.Backends.ValidateGeoACLNames(c.GeoACLs))
	require.NoError(t, Listeners(c))
}

func TestGeoStreamPoolMembers(t *testing.T) {
	const refused = "listener's alb relays to"
	stream := func(protocol string) *config.Config {
		c := config.NewConfig()
		c.Listeners["relay"] = listener.New("relay")
		c.Listeners["relay"].Protocol, c.Listeners["relay"].ListenPort = protocol, 9000
		pool := bo.New()
		pool.Provider, pool.ListenerName = bp.ALB, "relay"
		pool.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: "m1"}}}
		member := bo.New()
		member.Provider, member.OriginURL = bp.ReverseProxyShort, "tcp://member.example.com:9000"
		member.ListenerName = "relay"
		c.Backends = bo.Lookup{"pool": pool, "m1": member}
		geoACL(c, geoACLNorthAmerica, geolocopts.DefaultName, nil)
		return c
	}
	for _, protocol := range []string{listener.ProtocolTCP, listener.ProtocolTLS, listener.ProtocolUDP} {
		t.Run(protocol, func(t *testing.T) {
			// the alb dials its member without entering the member's routes, so its geo ACL is never judged
			c := stream(protocol)
			c.Backends["m1"].GeoACLName = geoACLNorthAmerica
			require.ErrorContains(t, validateGeoListeners(c), refused)
			// one on the alb judges each connection before a member is dialed
			c = stream(protocol)
			c.Backends["pool"].GeoACLName = geoACLNorthAmerica
			require.NoError(t, validateGeoListeners(c))
		})
	}

	// a member that serves HTTP requests too keeps path-level geo ACLs for them, but not a backend-level one
	c := stream(listener.ProtocolTCP)
	c.Backends["m1"].ListenerName, c.Backends["m1"].ListenerNames = "", []string{"relay", "default"}
	c.Backends["m1"].Paths = pathopts.List{{Path: "/api/", GeoACLName: geoACLNorthAmerica}}
	require.NoError(t, validateGeoListeners(c))
	c.Backends["m1"].GeoACLName = geoACLNorthAmerica
	require.ErrorContains(t, validateGeoListeners(c), refused)

	// so is a member of an alb in the pool
	c = stream(listener.ProtocolTCP)
	inner := bo.New()
	inner.Provider = bp.ALB
	inner.ALBOptions = &ao.Options{MechanismName: "rr", Pool: ao.PoolMemberList{{Name: "m2"}}}
	m2 := bo.New()
	m2.Provider, m2.OriginURL, m2.GeoACLName = bp.ReverseProxyShort, "tcp://m2.example.com:9000", geoACLNorthAmerica
	c.Backends["inner"], c.Backends["m2"] = inner, m2
	c.Backends["pool"].ALBOptions.Pool = append(c.Backends["pool"].ALBOptions.Pool, ao.PoolMember{Name: "inner"})
	require.ErrorContains(t, validateGeoListeners(c), `backend "m2": geo_acl_name is not supported`)

	// and a template that discovery clones into the pool
	c = stream(listener.ProtocolTCP)
	tmpl := bo.New()
	tmpl.Provider, tmpl.OriginURL, tmpl.IsTemplate = bp.ReverseProxyShort, "tcp://template.example.com:9000", true
	tmpl.GeoACLName = geoACLNorthAmerica
	c.Backends["template"] = tmpl
	c.Backends["pool"].ALBOptions.Discovery = &ao.DiscoveryOptions{DiscovererName: "dns", TemplateBackend: "template"}
	require.ErrorContains(t, validateGeoListeners(c), `backend "template": geo_acl_name is not supported`)
}
