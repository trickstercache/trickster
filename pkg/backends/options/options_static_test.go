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
	"errors"
	"path/filepath"
	"testing"

	taws "github.com/trickstercache/trickster/v2/pkg/aws"
	ao "github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	ino "github.com/trickstercache/trickster/v2/pkg/backends/influxdb/options"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	prop "github.com/trickstercache/trickster/v2/pkg/backends/prometheus/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	so "github.com/trickstercache/trickster/v2/pkg/backends/static/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"

	"go.yaml.in/yaml/v3"
)

func newStaticTestOptions(t *testing.T) *Options {
	t.Helper()
	o := New()
	o.Name = "site"
	o.Provider = providers.Static
	o.Static = so.New()
	o.Static.Root = t.TempDir()
	return o
}

func TestValidateStatic(t *testing.T) {
	o := newStaticTestOptions(t)
	// an authenticator and virtual hosting are the supported companions
	o.AuthenticatorName = "auth1"
	o.AuthOptions = &autho.Options{}
	o.Hosts = []string{"www.example.com"}
	if _, err := o.Validate(); err != nil {
		t.Errorf("expected a valid static backend, got %v", err)
	}

	o = newStaticTestOptions(t)
	o.Static = nil
	var missing *ErrMissingStaticOptions
	if _, err := o.Validate(); !errors.As(err, &missing) {
		t.Errorf("expected ErrMissingStaticOptions, got %v", err)
	}

	o = newStaticTestOptions(t)
	o.Static.Root = ""
	if _, err := o.Validate(); !errors.Is(err, so.ErrMissingRoot) {
		t.Errorf("expected ErrMissingRoot, got %v", err)
	}

	o = newStaticTestOptions(t)
	o.Provider = providers.ReverseProxyCache
	o.OriginURL = "http://example.com"
	var unsupported *ErrUnsupportedOption
	if _, err := o.Validate(); !errors.As(err, &unsupported) {
		t.Errorf("expected ErrUnsupportedOption for a static block on another provider, got %v", err)
	}
}

func TestValidateStaticUnsupportedOptions(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Options)
	}{
		{"paths", func(o *Options) { o.Paths = po.List{{Path: "/"}} }},
		{"req_rewriter_name", func(o *Options) { o.ReqRewriterName = "rewriter1" }},
		{"origin_url", func(o *Options) { o.OriginURL = "http://example.com" }},
		{"rule_name", func(o *Options) { o.RuleName = "rule1" }},
		{"alb", func(o *Options) { o.ALBOptions = &ao.Options{} }},
		{"prometheus", func(o *Options) { o.Prometheus = &prop.Options{} }},
		{"mysql", func(o *Options) { o.MySQL = mo.New() }},
		{"graphite", func(o *Options) { o.Graphite = &gro.Options{} }},
		{"influxdb", func(o *Options) { o.InfluxDB = &ino.Options{} }},
		{"sigv4", func(o *Options) { o.SigV4 = &taws.Options{} }},
		{"protocol", func(o *Options) { o.Protocol = "native" }},
		{"h2c_prior_knowledge", func(o *Options) { o.H2CPriorKnowledge = true }},
		{"preserve_host", func(o *Options) { o.PreserveHost = true }},
		{"proxy_only", func(o *Options) { o.ProxyOnly = true }},
		{"is_template", func(o *Options) { o.IsTemplate = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			o := newStaticTestOptions(t)
			test.mod(o)
			if _, err := o.Validate(); err == nil {
				t.Errorf("expected %s to be rejected on a static backend", test.name)
			}
		})
	}
}

func TestStaticCloneAndInitialize(t *testing.T) {
	o := newStaticTestOptions(t)
	o.Static.Root = "relative/site"
	o.Static.MIMETypes = map[string]string{"MD": "text/markdown"}
	if err := o.Initialize("site"); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(o.Static.Root) || o.Static.MIMETypes[".md"] == "" {
		t.Errorf("expected initialized static options, got %+v", o.Static)
	}
	c := o.Clone()
	c.Static.MIMETypes[".md"] = "text/plain"
	c.Static.Root = "/elsewhere"
	if o.Static.MIMETypes[".md"] != "text/markdown" || o.Static.Root == c.Static.Root {
		t.Error("expected cloned static options to be independent")
	}
}

func TestStaticFromYAML(t *testing.T) {
	root := t.TempDir()
	var l Lookup
	err := yaml.Unmarshal([]byte(`
site:
  provider: static
  authenticator_name: auth1
  static:
    root: `+root+`
    default_file: home.html
`), &l)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Initialize(); err != nil {
		t.Fatal(err)
	}
	if err = l.Validate(); err != nil {
		t.Fatal(err)
	}
	o := l["site"]
	if o.Static == nil || o.Static.Root != root || o.Static.DefaultFile != "home.html" ||
		o.Static.CacheControl != "" {
		t.Errorf("unexpected static options %+v", o.Static)
	}
	// static needs no cache, so none is assigned or required
	err = l.ValidateConfigMappings(nil, negative.Lookups{"default": negative.Lookup{}}, nil, nil,
		autho.Lookup{"auth1": &autho.Options{}}, tro.Lookup{"default": &tro.Options{}})
	if err != nil {
		t.Errorf("expected valid config mappings, got %v", err)
	}
	if o.AuthOptions == nil {
		t.Error("expected the authenticator to be attached")
	}
}
