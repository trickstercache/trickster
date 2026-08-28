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
	"strings"
	"testing"

	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
)

func TestValidateDiscoveryConfig(t *testing.T) {
	c, err := config.Load([]string{"-config", "../../../testdata/test.discovery.conf"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err = Validate(c); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	d, ok := c.Discovery["in-cluster"]
	if !ok || d == nil {
		t.Fatal("expected discoverer in-cluster")
	}
	if d.Kubernetes == nil || !d.Kubernetes.InCluster {
		t.Fatal("expected kubernetes in_cluster default")
	}
	alb := c.Backends["test-alb"]
	if alb == nil || alb.ALBOptions == nil || alb.ALBOptions.Discovery == nil {
		t.Fatal("expected alb discovery options")
	}
	if alb.ALBOptions.Discovery.StartupPolicy != ao.StartupPolicyRetry {
		t.Fatalf("expected default startup policy retry, got %q",
			alb.ALBOptions.Discovery.StartupPolicy)
	}
	if !c.Backends["prom-template"].IsTemplate {
		t.Fatal("expected prom-template to be a template")
	}
}

func TestValidateDiscoveryConfigFailures(t *testing.T) {
	tests := []struct {
		filename string
		expected string
	}{
		{
			"../../../testdata/test.discovery.bad-discoverer.conf",
			`invalid discoverer name "missing" provided in alb "test-alb"`,
		},
		{
			"../../../testdata/test.discovery.bad-template.conf",
			`invalid template_backend "static" provided in alb "test-alb"`,
		},
		{
			"../../../testdata/test.discovery.bad-provider.conf",
			`invalid provider "carrier-pigeon" for discoverer "my-disco"`,
		},
	}
	for _, test := range tests {
		t.Run(test.filename, func(t *testing.T) {
			c, err := config.Load([]string{"-config", test.filename})
			if err != nil {
				if !strings.Contains(err.Error(), test.expected) {
					t.Errorf("expected `%s` got `%s`", test.expected, err.Error())
				}
				return
			}
			err = Validate(c)
			if err == nil {
				t.Errorf("expected `%s` got nothing", test.expected)
			} else if !strings.Contains(err.Error(), test.expected) {
				t.Errorf("expected `%s` got `%s`", test.expected, err.Error())
			}
		})
	}
}
