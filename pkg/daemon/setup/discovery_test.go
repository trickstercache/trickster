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

package setup

import (
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	"github.com/trickstercache/trickster/v2/pkg/config"
	do "github.com/trickstercache/trickster/v2/pkg/discovery/options"
	dp "github.com/trickstercache/trickster/v2/pkg/discovery/providers"

	"github.com/stretchr/testify/require"
)

func discoveryTestConfig() *config.Config {
	c := config.NewConfig()
	delete(c.Backends, "default")
	tmpl := bo.New()
	tmpl.Provider = providers.Prometheus
	tmpl.IsTemplate = true
	tmpl.Name = "tmpl"
	albOpts := bo.New()
	albOpts.Provider = providers.ALB
	albOpts.Name = "alb1"
	albOpts.ALBOptions = &ao.Options{
		MechanismName: "rr",
		Discovery: &ao.DiscoveryOptions{
			DiscovererName:  "d1",
			TemplateBackend: "tmpl",
			Query:           &do.Query{Service: "svc"},
		},
	}
	c.Backends["tmpl"] = tmpl
	c.Backends["alb1"] = albOpts
	c.Discovery = do.Lookup{
		"d1": &do.Options{Name: "d1", Provider: dp.Kubernetes},
	}
	return c
}

func TestDiscoveryConfigUnchanged(t *testing.T) {
	oldConf := discoveryTestConfig()
	newConf := discoveryTestConfig()
	opts := newConf.Backends["alb1"].ALBOptions.Discovery

	require.True(t, discoveryConfigUnchanged(oldConf, newConf, "alb1", opts))
	require.False(t, discoveryConfigUnchanged(nil, newConf, "alb1", opts),
		"startup (no prior config) must not claim unchanged")

	// alb discovery block changed
	changed := discoveryTestConfig()
	changed.Backends["alb1"].ALBOptions.Discovery.MinMembers = 3
	require.False(t, discoveryConfigUnchanged(oldConf, changed, "alb1",
		changed.Backends["alb1"].ALBOptions.Discovery))

	// discoverer connection settings changed
	changed = discoveryTestConfig()
	changed.Discovery["d1"].Kubernetes = &do.KubernetesOptions{Kubeconfig: "/k"}
	require.False(t, discoveryConfigUnchanged(oldConf, changed, "alb1",
		changed.Backends["alb1"].ALBOptions.Discovery))

	// template backend changed
	changed = discoveryTestConfig()
	changed.Backends["tmpl"].Timeout = 1
	require.False(t, discoveryConfigUnchanged(oldConf, changed, "alb1",
		changed.Backends["alb1"].ALBOptions.Discovery))

	// alb missing from the old config
	renamed := discoveryTestConfig()
	delete(renamed.Backends, "alb1")
	require.False(t, discoveryConfigUnchanged(renamed, newConf, "alb1", opts))
}
