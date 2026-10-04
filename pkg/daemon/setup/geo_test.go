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

package setup

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends"
	"github.com/trickstercache/trickster/v2/pkg/config"
	kubecfg "github.com/trickstercache/trickster/v2/pkg/config/kubernetes"
	"github.com/trickstercache/trickster/v2/pkg/daemon/instance"
	geoacl "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl"
	geoaclopts "github.com/trickstercache/trickster/v2/pkg/proxy/geo/acl/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/listener"

	"github.com/stretchr/testify/require"
)

const geoSetupConfig = `
backends:
  test:
    provider: rp
    origin_url: 'http://example.com'
    geo_acl_name: %s
    paths:
      - path: /open/
        geo_acl_name: none
geo_locators:
  default:
    provider: geofeed
    geofeed:
      entries: ["%s"]
  unused:
    provider: geofeed
    geofeed:
      entries: ["198.51.100.0/24,FR"]
geo_acls:
  us-only:
    allow: [US]
  no-us:
    deny: [US]
  unattached:
    geo_locator_name: unused
    deny: [FR]
`

func applyGeoConfig(t *testing.T, si *instance.ServerInstance, aclName, feedEntry string) *config.Config {
	t.Helper()
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, fmt.Sprintf(geoSetupConfig, aclName, feedEntry)))
	require.NoError(t, err)
	quietListeners(conf)
	require.NoError(t, ApplyConfig(si, conf, clients, nil, nil, si.Listeners))
	return conf
}

func TestApplyConfigGeo(t *testing.T) {
	group := listener.NewGroup()
	t.Cleanup(func() { _ = group.Shutdown(0) })
	si := &instance.ServerInstance{Listeners: group}
	t.Cleanup(func() { Shutdown(si) })

	conf := applyGeoConfig(t, si, "us-only", "192.0.2.0/24,US")
	require.Len(t, si.GeoLocators, 1, "only a locator an attached geo ACL names is opened")
	first := si.GeoLocators.Locator("default")
	require.NotNil(t, first)
	a, ok := conf.GeoACLs["us-only"].Compiled.(*geoacl.ACL)
	require.True(t, ok)
	require.Nil(t, conf.GeoACLs["unattached"].Compiled)

	// the route answers by its geo ACL
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://trickster/", nil)
	r.RemoteAddr = "198.51.100.1:1000"
	si.Config.Backends["test"].Router.ServeHTTP(w, r)
	require.Equal(t, a.Response().Status, w.Code)

	// a reload keeps a locator whose options are unchanged, and replaces one whose options changed
	applyGeoConfig(t, si, "no-us", "192.0.2.0/24,US")
	require.Same(t, first, si.GeoLocators.Locator("default"))
	applyGeoConfig(t, si, "no-us", "192.0.2.0/24,CA")
	require.NotSame(t, first, si.GeoLocators.Locator("default"))

	// a configuration with no geo ACL attached keeps no locator
	conf, clients, err := BootstrapConfig("-config", writeConfig(t, minimalConfig))
	require.NoError(t, err)
	quietListeners(conf)
	require.NoError(t, ApplyConfig(si, conf, clients, nil, nil, group))
	require.Empty(t, si.GeoLocators)

	applyGeoConfig(t, si, "us-only", "192.0.2.0/24,US")
	Shutdown(si)
	require.Nil(t, si.GeoLocators)
}

func TestApplyConfigGeoFailure(t *testing.T) {
	si := &instance.ServerInstance{Listeners: listener.NewGroup()}
	t.Cleanup(func() { Shutdown(si) })
	applyGeoConfig(t, si, "us-only", "192.0.2.0/24,US")
	serving := si.GeoLocators

	// a locator that fails to build fails the apply, which leaves the serving locators in place
	conf, clients, err := BootstrapConfig("-config", writeConfig(t,
		fmt.Sprintf(geoSetupConfig, "us-only", "192.0.2.0/24,CA")))
	require.NoError(t, err)
	quietListeners(conf)
	conf.GeoLocators["default"].Geofeed.Entries = []string{"bad line"}
	err = ApplyConfig(si, conf, clients, nil, nil, si.Listeners)
	require.ErrorContains(t, err, "invalid geofeed line")
	require.Equal(t, serving, si.GeoLocators)
}

func TestAttachedGeoACLs(t *testing.T) {
	require.Nil(t, attachedGeoACLs(nil))
	conf, _, err := BootstrapConfig("-config", writeConfig(t, fmt.Sprintf(geoSetupConfig, "us-only",
		"192.0.2.0/24,US")))
	require.NoError(t, err)
	names := func() string {
		var out []string
		for _, o := range attachedGeoACLs(conf) {
			out = append(out, o.Name)
		}
		return strings.Join(out, ",")
	}
	require.Equal(t, "us-only", names())
	conf.Backends["test"].Paths[0].GeoACLName = "no-us"
	conf.Backends["test"].Paths[0].GeoACLOptions = conf.GeoACLs["no-us"]
	require.Equal(t, "no-us,us-only", names())
	// the controller may name any geo ACL, so every one is compiled
	conf.Kubernetes = &kubecfg.Options{}
	require.Equal(t, "no-us,unattached,us-only", names())
	conf.GeoACLs["empty"] = (*geoaclopts.Options)(nil)
	require.Equal(t, "no-us,unattached,us-only", names())
}

type gateTaker struct {
	listener.ProtocolServer
	gate backends.SessionGate
}

func (g *gateTaker) UpdateSessionGate(gate backends.SessionGate) { g.gate = gate }

type gateRefuser struct {
	listener.ProtocolServer
}

func TestSessionGateFor(t *testing.T) {
	conf, _, err := BootstrapConfig("-config", writeConfig(t, fmt.Sprintf(geoSetupConfig, "us-only",
		"192.0.2.0/24,US")))
	require.NoError(t, err)
	require.Nil(t, sessionGateFor(nil, "default"))
	require.Nil(t, sessionGateFor(conf, "default"), "a geo ACL that is not compiled gates nothing")
	si := &instance.ServerInstance{}
	t.Cleanup(func() { si.GeoLocators.Close() })
	si.GeoLocators, err = buildGeo(si, conf)
	require.NoError(t, err)
	gate := sessionGateFor(conf, "default")
	require.NotNil(t, gate)
	require.Nil(t, sessionGateFor(conf, "elsewhere"))

	taker := &gateTaker{}
	require.True(t, setSessionGate(taker, gate))
	require.Equal(t, gate, taker.gate)
	require.True(t, setSessionGate(&gateRefuser{}, nil))
	require.False(t, setSessionGate(&gateRefuser{}, gate), "a server that takes no gate must not serve a gated listener")
}
