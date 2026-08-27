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
	"fmt"
	"strings"
	"testing"
	"time"

	gro "github.com/trickstercache/trickster/v2/pkg/backends/graphite/options"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	mo "github.com/trickstercache/trickster/v2/pkg/backends/mysql/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	corso "github.com/trickstercache/trickster/v2/pkg/proxy/cors/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type testOptions struct {
	Backends Lookup `yaml:"backends,omitempty"`
}

func fromYAML(conf, name string) (*Options, error) {
	to := &testOptions{}

	err := yaml.Unmarshal([]byte(conf), to)
	if err != nil {
		return nil, err
	}

	if o, ok := to.Backends[name]; ok {
		return o, err
	}
	for k, o := range to.Backends {
		o.Name = k
		return o, err
	}
	return nil, nil
}

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil options")
	}
	if o.FetchConcurrencyLimit != DefaultFetchConcurrencyLimit {
		t.Errorf("expected FetchConcurrencyLimit=%d, got %d",
			DefaultFetchConcurrencyLimit, o.FetchConcurrencyLimit)
	}
}

func TestMySQLLimitsYAMLDefaultsCloneAndValidation(t *testing.T) {
	o, err := fromYAML(`
backends:
  mysql1:
    provider: mysql
    origin_url: mysql://user:password@example.com/database
    mysql:
      max_result_rows: 42
`, "mysql1")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Initialize("mysql1"); err != nil {
		t.Fatal(err)
	}
	if o.MySQL == nil || o.MySQL.MaxResultRows != 42 ||
		o.MySQL.MaxResultSizeBytes != mo.DefaultMaxResultSizeBytes {
		t.Fatalf("unexpected MySQL options: %#v", o.MySQL)
	}
	clone := o.Clone()
	clone.MySQL.MaxResultRows++
	if o.MySQL.MaxResultRows != 42 {
		t.Fatal("clone mutated original MySQL options")
	}
}

// TestProviderSizingDefaults covers the provider-specific sizing defaults:
// Graphite caches at the origin's native resolution, so it needs a larger
// object limit and a longer retention than the generic backend defaults,
// and an operator must not have to discover that to get a working backend.
// The defaults apply only where the configuration is silent.
func TestProviderSizingDefaults(t *testing.T) {
	mos, trf := GetProviderDefaults(providers.Graphite)
	if mos != gro.DefaultMaxObjectSizeBytes || trf != gro.DefaultTimeseriesRetentionFactor {
		t.Errorf("graphite defaults: got (%d, %d)", mos, trf)
	}
	mos, trf = GetProviderDefaults(providers.Prometheus)
	if mos != DefaultMaxObjectSizeBytes || trf != DefaultBackendTRF {
		t.Errorf("prometheus defaults: got (%d, %d)", mos, trf)
	}
	if mos, trf = GetProviderDefaults(""); mos != DefaultMaxObjectSizeBytes || trf != DefaultBackendTRF {
		t.Errorf("unknown provider must take the generic defaults: got (%d, %d)", mos, trf)
	}

	tests := []struct {
		name     string
		yaml     string
		wantSize int
		wantTRF  int
	}{
		{
			name: "graphite silent",
			yaml: `
backends:
  graphite1:
    provider: graphite
    origin_url: http://example.com:80
`,
			wantSize: gro.DefaultMaxObjectSizeBytes,
			wantTRF:  gro.DefaultTimeseriesRetentionFactor,
		},
		{
			name: "graphite explicit wins",
			yaml: `
backends:
  graphite1:
    provider: graphite
    origin_url: http://example.com:80
    max_object_size_bytes: 1024
    timeseries_retention_factor: 7
`,
			wantSize: 1024,
			wantTRF:  7,
		},
		{
			name: "graphite explicit zeros survive",
			yaml: `
backends:
  graphite1:
    provider: graphite
    origin_url: http://example.com:80
    max_object_size_bytes: 0
    timeseries_retention_factor: 0
`,
			wantSize: 0,
			wantTRF:  0,
		},
		{
			name: "prometheus explicit zero survives",
			yaml: `
backends:
  prom1:
    provider: prometheus
    origin_url: http://example.com:9090
    max_object_size_bytes: 0
`,
			wantSize: 0,
			wantTRF:  DefaultBackendTRF,
		},
		{
			// the generic default, named explicitly: the provider default
			// must not overwrite a value the operator actually chose
			name: "graphite explicitly generic",
			yaml: `
backends:
  graphite1:
    provider: graphite
    origin_url: http://example.com:80
    max_object_size_bytes: 524288
    timeseries_retention_factor: 1024
`,
			wantSize: DefaultMaxObjectSizeBytes,
			wantTRF:  DefaultBackendTRF,
		},
		{
			name: "other providers unchanged",
			yaml: `
backends:
  prom1:
    provider: prometheus
    origin_url: http://example.com:9090
`,
			wantSize: DefaultMaxObjectSizeBytes,
			wantTRF:  DefaultBackendTRF,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := "graphite1"
			if strings.Contains(tc.yaml, "prom1:") {
				name = "prom1"
			}
			o, err := fromYAML(tc.yaml, name)
			if err != nil {
				t.Fatal(err)
			}
			if err := o.Initialize(name); err != nil {
				t.Fatal(err)
			}
			if o.MaxObjectSizeBytes != tc.wantSize {
				t.Errorf("max_object_size_bytes: got %d, want %d", o.MaxObjectSizeBytes, tc.wantSize)
			}
			if o.TimeseriesRetentionFactor != tc.wantTRF {
				t.Errorf("timeseries_retention_factor: got %d, want %d",
					o.TimeseriesRetentionFactor, tc.wantTRF)
			}
			if o.TimeseriesRetention != timeconv.Duration(tc.wantTRF) {
				t.Errorf("timeseries_retention: got %v, want %v",
					o.TimeseriesRetention, timeconv.Duration(tc.wantTRF))
			}
			// the sizing survives a clone, which is how a reload carries it
			if c := o.Clone(); c.MaxObjectSizeBytes != tc.wantSize ||
				c.TimeseriesRetentionFactor != tc.wantTRF {
				t.Errorf("clone lost the sizing: %d %d", c.MaxObjectSizeBytes, c.TimeseriesRetentionFactor)
			}
		})
	}

	for _, provider := range []string{providers.Graphite, providers.Prometheus} {
		o := New()
		o.Provider = provider
		o.OriginURL = "http://example.com:80"
		o.MaxObjectSizeBytes = 12345
		o.TimeseriesRetentionFactor = 99
		if err := o.Initialize("b1"); err != nil {
			t.Fatal(err)
		}
		if o.MaxObjectSizeBytes != 12345 || o.TimeseriesRetentionFactor != 99 {
			t.Errorf("%s: Initialize overwrote programmatic sizing: (%d, %d)",
				provider, o.MaxObjectSizeBytes, o.TimeseriesRetentionFactor)
		}
		if o.TimeseriesRetention != timeconv.Duration(99) {
			t.Errorf("%s: retention not derived: %v", provider, o.TimeseriesRetention)
		}
	}
	o := New()
	o.Provider = providers.Graphite
	o.OriginURL = "http://example.com:80"
	if err := o.Initialize("graphite1"); err != nil {
		t.Fatal(err)
	}
	if o.MaxObjectSizeBytes != DefaultMaxObjectSizeBytes ||
		o.TimeseriesRetentionFactor != DefaultBackendTRF {
		t.Errorf("programmatic backend must keep generic defaults: (%d, %d)",
			o.MaxObjectSizeBytes, o.TimeseriesRetentionFactor)
	}
	o.ApplyProviderSizingDefaults()
	if o.MaxObjectSizeBytes != gro.DefaultMaxObjectSizeBytes ||
		o.TimeseriesRetentionFactor != gro.DefaultTimeseriesRetentionFactor {
		t.Errorf("ApplyProviderSizingDefaults: got (%d, %d)", o.MaxObjectSizeBytes, o.TimeseriesRetentionFactor)
	}
}

// TestProviderSizingDefaultsMergeKeys covers YAML merge semantics in
// presence detection: values inherited through `<<` aliases (including
// alias sequences) were applied by the decoder and must count as explicit,
// or an inherited explicit zero would be silently replaced by a provider
// default. Local keys override merged ones as YAML specifies.
func TestProviderSizingDefaultsMergeKeys(t *testing.T) {
	load := func(t *testing.T, doc, name string) *Options {
		t.Helper()
		var c struct {
			Backends Lookup `yaml:"backends"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(doc), &c))
		o, ok := c.Backends[name]
		require.True(t, ok, "backend %s not decoded", name)
		require.NoError(t, o.Initialize(name))
		return o
	}

	t.Run("inherited explicit zeros survive", func(t *testing.T) {
		o := load(t, `
defaults: &defaults
  max_object_size_bytes: 0
  timeseries_retention_factor: 0
backends:
  graphite1:
    <<: *defaults
    provider: graphite
    origin_url: http://graphite:80
`, "graphite1")
		if o.MaxObjectSizeBytes != 0 || o.TimeseriesRetentionFactor != 0 {
			t.Errorf("inherited zeros replaced: (%d, %d)", o.MaxObjectSizeBytes, o.TimeseriesRetentionFactor)
		}
	})

	t.Run("inherited non-zero survives for an existing provider", func(t *testing.T) {
		o := load(t, `
defaults: &defaults
  max_object_size_bytes: 777
backends:
  prom1:
    <<: *defaults
    provider: prometheus
    origin_url: http://prom:9090
`, "prom1")
		if o.MaxObjectSizeBytes != 777 || o.TimeseriesRetentionFactor != DefaultBackendTRF {
			t.Errorf("inherited value lost: (%d, %d)", o.MaxObjectSizeBytes, o.TimeseriesRetentionFactor)
		}
	})

	t.Run("merge sequence", func(t *testing.T) {
		o := load(t, `
a: &a
  max_object_size_bytes: 111
b: &b
  timeseries_retention_factor: 222
backends:
  graphite1:
    <<: [*a, *b]
    provider: graphite
    origin_url: http://graphite:80
`, "graphite1")
		if o.MaxObjectSizeBytes != 111 || o.TimeseriesRetentionFactor != 222 {
			t.Errorf("merge sequence lost: (%d, %d)", o.MaxObjectSizeBytes, o.TimeseriesRetentionFactor)
		}
	})

	t.Run("local key overrides merged", func(t *testing.T) {
		o := load(t, `
defaults: &defaults
  max_object_size_bytes: 111
backends:
  graphite1:
    <<: *defaults
    provider: graphite
    origin_url: http://graphite:80
    max_object_size_bytes: 333
`, "graphite1")
		if o.MaxObjectSizeBytes != 333 {
			t.Errorf("local override lost: %d", o.MaxObjectSizeBytes)
		}
	})

	t.Run("quoted literal << is not a merge", func(t *testing.T) {
		// a quoted "<<" key has the string tag: the decoder does not merge
		// it, so its contents must not mark the sizing fields explicit
		o := load(t, `
backends:
  graphite1:
    "<<": {max_object_size_bytes: 1}
    provider: graphite
    origin_url: http://graphite:80
`, "graphite1")
		if o.MaxObjectSizeBytes != gro.DefaultMaxObjectSizeBytes {
			t.Errorf("quoted << treated as a merge: %d", o.MaxObjectSizeBytes)
		}
	})

	t.Run("silent through merge still gets provider default", func(t *testing.T) {
		o := load(t, `
defaults: &defaults
  timeout: 30s
backends:
  graphite1:
    <<: *defaults
    provider: graphite
    origin_url: http://graphite:80
`, "graphite1")
		if o.MaxObjectSizeBytes != gro.DefaultMaxObjectSizeBytes {
			t.Errorf("provider default not applied: %d", o.MaxObjectSizeBytes)
		}
	})
}

func TestCORSOptionsYAML(t *testing.T) {
	o, err := fromYAML(`
backends:
  test:
    provider: reverseproxycache
    origin_url: http://example.com
    cors:
      mode: merge
      headers:
        Access-Control-Allow-Origin: https://trickster.example.com
    paths:
      - path: /private
        cors:
          mode: disable
`, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Initialize("test"); err != nil {
		t.Fatal(err)
	}
	if o.CORS == nil || o.CORS.Mode != corso.ModeMerge {
		t.Fatalf("backend CORS mode = %v, want %q", o.CORS, corso.ModeMerge)
	}
	if got := o.CORS.Headers[headers.NameAllowOrigin]; got != "https://trickster.example.com" {
		t.Fatalf("backend allow origin = %q", got)
	}
	if len(o.Paths) != 1 || o.Paths[0].CORS == nil || o.Paths[0].CORS.Mode != corso.ModeDisable {
		t.Fatalf("path CORS = %v, want disable policy", o.Paths)
	}
	if o.Paths[0].CORS.Headers != nil {
		t.Fatalf("disabled path CORS headers = %v, want nil", o.Paths[0].CORS.Headers)
	}
	if ok, err := o.Validate(); !ok || err != nil {
		t.Fatalf("Validate() = %v, %v", ok, err)
	}

	clone := o.Clone()
	clone.CORS.Headers[headers.NameAllowOrigin] = "https://other.example.com"
	if o.CORS.Headers[headers.NameAllowOrigin] != "https://trickster.example.com" {
		t.Fatal("clone mutated backend CORS headers")
	}
}

func TestAccessLogOptionsYAML(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Fatal(err)
	}
	al := o.AccessLog
	if al == nil {
		t.Fatal("expected access_log options")
	}
	if al.Filename != "/tmp/test.access.log" || al.Format != "combined" ||
		al.Rotation == nil || *al.Rotation.Size != 64*1024*1024 ||
		al.Retention == nil || *al.Retention.Count != 3 ||
		al.ErrorFilename != "/tmp/test.error.log" || al.ErrorThreshold != 500 {
		t.Fatalf("unexpected access_log options: %+v", al)
	}
	clone := o.Clone()
	*clone.AccessLog.Retention.Count = 9
	if *o.AccessLog.Retention.Count != 3 {
		t.Fatal("clone mutated access_log retention")
	}
	o.AccessLog.Format = "%Z"
	if _, err := o.Validate(); err == nil {
		t.Fatal("expected validation error for invalid access log format")
	}
}

func TestClone(t *testing.T) {
	p := po.New()
	o := New()
	o.Hosts = []string{"test"}
	o.CacheName = "test"
	o.CompressibleTypes = sets.New([]string{"test"})
	o.Paths = po.List{p}
	o.NegativeCache = map[int]time.Duration{1: 1}
	o.HealthCheck = &ho.Options{}
	o.FastForwardPath = p
	o.RuleOptions = &ro.Options{}
	o.ReplicaGroup = "replicas-a"
	o2 := o.Clone()
	if o2.CacheName != "test" {
		t.Error("clone failed")
	}
	if o2.ReplicaGroup != o.ReplicaGroup {
		t.Errorf("clone replica group = %q, want %q", o2.ReplicaGroup, o.ReplicaGroup)
	}
}

func TestReplicaGroupYAMLCloneAndInitialization(t *testing.T) {
	o, err := fromYAML(`
backends:
  prom-a:
    provider: prometheus
    origin_url: http://example.com
    replica_group: "  shard-a  "
`, "prom-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.Initialize("prom-a"); err != nil {
		t.Fatal(err)
	}
	if o.ReplicaGroup != "shard-a" {
		t.Fatalf("replica group = %q, want shard-a", o.ReplicaGroup)
	}
	if clone := o.CloneYAMLSafe(); clone.ReplicaGroup != "shard-a" {
		t.Fatalf("YAML-safe clone replica group = %q", clone.ReplicaGroup)
	}
	if out := o.ToYAML(); !strings.Contains(out, "replica_group: shard-a") {
		t.Fatalf("round-trip YAML missing replica_group:\n%s", out)
	}

	unset := New()
	if err := unset.Initialize("prom-b"); err != nil {
		t.Fatal(err)
	}
	if unset.ReplicaGroup != "prom-b" {
		t.Fatalf("default replica group = %q, want prom-b", unset.ReplicaGroup)
	}
	if out := unset.ToYAML(); strings.Contains(out, "replica_group:") {
		t.Fatalf("implicit replica group should not be exported:\n%s", out)
	}

	nested := New()
	nested.Provider = providers.ALB
	nested.ReplicaGroup = "shard-a"
	if err := nested.Initialize("nested-a"); err != nil {
		t.Fatalf("nested ALB replica group rejected: %v", err)
	}

	unsupported := New()
	unsupported.Provider = providers.ReverseProxyShort
	unsupported.ReplicaGroup = "shard-a"
	if err := unsupported.Initialize("proxy-a"); err == nil {
		t.Fatal("expected replica group on reverse proxy backend to be rejected")
	}
}

func TestValidateGraphiteOriginAuth(t *testing.T) {
	newBackend := func(g *gro.Options, paths po.List) *Options {
		o := New()
		o.Name = "test"
		o.Provider = providers.Graphite
		o.OriginURL = "http://example.com"
		o.Graphite = g
		o.Paths = paths
		return o
	}
	tests := []struct {
		name string
		o    *Options
		err  error
	}{
		{"valid credential", newBackend(
			&gro.Options{OriginAuthorization: "Bearer tok"}, nil), nil},
		{"authorization with username", newBackend(
			&gro.Options{OriginAuthorization: "Bearer tok", OriginUsername: "u"}, nil),
			gro.ErrOriginAuthConflict},
		{"password without username", newBackend(
			&gro.Options{OriginPassword: "p"}, nil), gro.ErrOriginAuthNoUser},
		{"credential with +Authorization path", newBackend(
			&gro.Options{OriginUsername: "u", OriginPassword: "p"},
			po.List{{Path: "/render",
				RequestHeaders: map[string]string{"+authorization": "x"}}}),
			gro.ErrOriginAuthAppend},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.o.Validate()
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected %v, got %v", tc.err, err)
			}
		})
	}
}

func TestValidateBackendName(t *testing.T) {
	err := ValidateBackendName("test")
	if err != nil {
		t.Error(err)
	}

	err = ValidateBackendName("frontend")
	if err == nil {
		t.Error("expected error for invalid backend name")
	}
}

func TestValidateConfigMappings(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	ol := Lookup{o.Name: o}
	ol["frontend"] = o.Clone()
	o.Provider = "rpc"

	err = ol.ValidateConfigMappings(co.Lookup{}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Error("expected error for invalid cache name")
	}

	delete(ol, "frontend")
	o.Provider = providers.Rule
	o.RuleName = "test"
	err = ol.ValidateConfigMappings(co.Lookup{"test": nil}, negative.Lookups{},
		ro.Lookup{}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Error("expected error for invalid rule name")
	}

	err = ol.ValidateConfigMappings(co.Lookup{"test": nil}, negative.Lookups{},
		ro.Lookup{"test": new(ro.Options)}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Error("expected error for invalid tracing name")
	}

	o.TracingConfigName = ""

	o.Name = ""
	err = ol.ValidateConfigMappings(co.Lookup{"test": nil}, negative.Lookups{},
		ro.Lookup{"test": new(ro.Options)}, rwopts.Lookup{}, autho.Lookup{},
		tro.Lookup{})
	if err == nil {
		t.Error("expected error for invalid backend name")
	}

	o.Name = "test"
	o.Provider = providers.ALB
	o.RuleName = ""
	err = ol.ValidateConfigMappings(co.Lookup{"test": nil}, negative.Lookups{},
		ro.Lookup{"test": new(ro.Options)}, rwopts.Lookup{}, autho.Lookup{}, tro.Lookup{})
	if err == nil {
		t.Error("expected error for invalid negative cache name")
	}

	o.NegativeCacheName = ""
	tpm := o.Clone()
	tpm.Name = "test_pool_member"
	tpm.Provider = "rp"
	tpm.ALBOptions = nil
	ol["test_pool_member"] = tpm

	err = ol.ValidateConfigMappings(co.Lookup{"test": nil}, negative.Lookups{},
		ro.Lookup{"test": new(ro.Options)}, rwopts.Lookup{}, autho.Lookup{},
		tro.Lookup{})
	if err != nil {
		t.Error(err)
	}
}

func testStringValueValidationError(to *testOptions, location *string, testValue string) error {
	// Test Invalid String
	s := *location
	*location = testValue
	err := to.Backends.Validate()
	*location = s // restore original string
	return err
}

type durationSwapper struct {
	location   *timeconv.Duration
	restoreVal timeconv.Duration
	testValue  timeconv.Duration
}

func testDurationValueValidationError(to *testOptions, sws []durationSwapper) error {
	for i := range sws {
		sws[i].restoreVal = *sws[i].location
		*sws[i].location = sws[i].testValue
	}
	err := Lookup(to.Backends).Validate()
	for i := range sws {
		*sws[i].location = sws[i].restoreVal
	}
	return err
}

func TestValidate(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	o.Name = "test"
	if err := o.Initialize("test"); err != nil {
		t.Error(err)
	}

	o2, err := fromYAML(testYAML, "test_pool_member")
	o2.Name = "test_pool_member"
	if err := o2.Initialize("test_pool_member"); err != nil {
		t.Error(err)
	}
	to := &testOptions{Backends: Lookup{o.Name: o, o2.Name: o2}}

	errType02 := NewErrMissingOriginURL("test").(*ErrMissingOriginURL)
	errType03 := NewErrMissingProvider("test").(*ErrMissingProvider)

	// string value tests
	tests := []struct {
		to       *testOptions
		loc      *string
		val      string
		expected any
	}{
		{ // 0 - valid negative cache name
			to:       to,
			loc:      &o.NegativeCacheName,
			val:      "test",
			expected: nil,
		},
		{ // 1 - invalid origin URL
			to:       to,
			loc:      &o.OriginURL,
			val:      "",
			expected: errType02,
		},
		{ // 2 - valid origin URL + strip trailing slash
			to:       to,
			loc:      &o.OriginURL,
			val:      "http://trickstercache.org/test/path/",
			expected: nil,
		},
		{ // 3 - invalid cache key prefix
			to:       to,
			loc:      &o.CacheKeyPrefix,
			val:      "",
			expected: nil,
		},
		{ // 4 - invalid provider
			to:       to,
			loc:      &o.Provider,
			val:      "",
			expected: errType03,
		},
		{ // 5 - invalid name
			to:       to,
			loc:      &o.Name,
			val:      "",
			expected: nil,
		},
	}

	for i, test := range tests {
		t.Run(fmt.Sprintf("strings %d", i), func(t *testing.T) {
			err = testStringValueValidationError(test.to, test.loc, test.val)
			if err == nil && test.expected == nil {
				return
			}

			if err == nil && test.expected != nil {
				t.Errorf("expected [%s] got nil", test.expected)
			}

			if err != nil && test.expected == nil {
				t.Errorf("expected nil got [%s]", err)
			}

			if !errors.As(err, &test.expected) {
				t.Errorf("expected [%s] got [%s]", test.expected, err)
			}
		})
	}

	// duration value tests
	tests2 := []struct {
		to       *testOptions
		sw       []durationSwapper
		expected any
	}{
		{ // case 0 - verifies: if ShardStep > 0 && MaxShardSize == 0 { MaxShardSize = ShardStep }
			to: to,
			sw: []durationSwapper{
				{
					location:  &o.ShardStep,
					testValue: timeconv.Duration(1),
				},
			},
			expected: nil,
		},
		{ // case 2 - verifies: if MaxShardSize % ShardStep != 0 { return ErrInvalidMaxShardSizeMS }
			to: to,
			sw: []durationSwapper{
				{
					location:  &o.MaxShardSizeTime,
					testValue: timeconv.Duration(10),
				},
				{
					location:  &o.ShardStep,
					testValue: timeconv.Duration(32),
				},
			},
			expected: ErrInvalidMaxShardSizeTime,
		},
	}

	for i, test := range tests2 {
		t.Run(fmt.Sprintf("ints %d", i), func(t *testing.T) {
			err = testDurationValueValidationError(test.to, test.sw)
			if err == nil && test.expected == nil {
				return
			}

			if err == nil && test.expected != nil {
				t.Errorf("expected [%s] got nil", test.expected)
			}

			if err != nil && test.expected == nil {
				t.Errorf("expected nil got [%s]", err)
			}

			if !errors.As(err, &test.expected) {
				t.Errorf("expected [%s] got [%s]", test.expected, err)
			}
		})
	}

	t.Run("maxShard edge cases", func(t *testing.T) {
		opts := *o
		opts.MaxShardSizeTime = timeconv.Duration(1 * time.Millisecond)
		opts.MaxShardSizePoints = 1
		to := &testOptions{Backends: Lookup{o.Name: &opts}}
		require.ErrorIs(t, Lookup(to.Backends).Validate(), ErrInvalidMaxShardSize)
	})
}

func TestInitialize(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}

	err = o.Initialize("test")
	if err != nil {
		t.Error(err)
	}

	oInvalid := *o
	oInvalid.MaxQueryRange = timeconv.Duration(-1 * time.Hour)
	if err := oInvalid.Initialize("test_invalid"); err == nil {
		t.Error("expected error for negative max_query_range, got nil")
	}

	o2, err := fromTestYAMLWithDefault()
	if err != nil {
		t.Error(err)
	}

	err = o2.Initialize("test")
	if err != nil {
		t.Error(err)
	}

	o2, err = fromTestYAMLWithReqRewriter()
	if err != nil {
		t.Error(err)
	}

	err = o2.Initialize("test")
	if err != nil {
		t.Error(err)
	}

	o2, err = fromTestYAMLWithALB()
	if err != nil {
		t.Error(err)
	}

	err = o2.Initialize("test")
	if err != nil {
		t.Error(err)
	}
}

func TestValidateTLSConfigs(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}

	l := Lookup{o.Name: o}

	b, err := l.ValidateTLSConfigs()
	if err == nil {
		t.Error("expected error")
	}
	if b {
		t.Error("expected false")
	}

	caFile := t.TempDir() + "/test.rootca.01.pem"
	keyFile := t.TempDir() + "/test.01.key.pem"
	certFile := t.TempDir() + "/test.01.cert.pem"

	err = tlstest.WriteTestKeyAndCert(true, "", caFile)
	if err != nil {
		t.Error(err)
	}

	err = tlstest.WriteTestKeyAndCert(false, keyFile, certFile)
	if err != nil {
		t.Error(err)
	}

	o.TLS.CertificateAuthorityPaths = []string{caFile}
	o.TLS.PrivateKeyPath = keyFile
	o.TLS.FullChainCertPath = certFile
	o.TLS.ClientCertPath = certFile
	o.TLS.ClientKeyPath = keyFile

	b, err = l.ValidateTLSConfigs()
	if err != nil {
		t.Error(err)
	}

	if !b {
		t.Error("expected true")
	}
}

func TestCloneYAMLSafe(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}

	var p *po.Options
	for _, path := range o.Paths {
		if path.Path == "/series" {
			p = path
			break
		}
	}
	if p == nil {
		t.Error("expected '/series' path")
	}
	p.RequestHeaders = map[string]string{headers.NameAuthorization: "trickster"}

	o2 := o.CloneYAMLSafe()
	var p2 *po.Options
	for _, path := range o2.Paths {
		if path.Path == "/series" {
			p2 = path
			break
		}
	}
	if p2 == nil {
		t.Error("expected '/series' path")
	}

	if v, ok := p2.RequestHeaders[headers.NameAuthorization]; !ok || v != "*****" {
		t.Error("expected *****")
	}

	p2.RequestHeaders = map[string]string{headers.NameAuthorization: "trickster"}
}

func TestCloneYAMLSafeMasksAllAuthorizationForms(t *testing.T) {
	o := New()
	o.Name = "test"
	o.Provider = providers.Graphite
	o.OriginURL = "http://example.com"
	o.Paths = po.List{{Path: "/render", RequestHeaders: map[string]string{
		"authorization":  "Bearer path-secret",
		"+Authorization": "Bearer append-secret",
		"-authorization": "x",
	}}}
	o.HealthCheck = &ho.Options{Headers: map[string]string{
		"authorization": "Bearer probe-secret",
		"X-Probe":       "trickster",
	}}

	got := o.CloneYAMLSafe()
	for k, v := range got.Paths[0].RequestHeaders {
		if v != "*****" {
			t.Errorf("path header %q not masked: %q", k, v)
		}
	}
	if v := got.HealthCheck.Headers["authorization"]; v != "*****" {
		t.Errorf("health header not masked: %q", v)
	}
	if v := got.HealthCheck.Headers["X-Probe"]; v != "trickster" {
		t.Errorf("non-sensitive health header must be preserved: %q", v)
	}
	// no credential form may survive into the YAML text either
	y := o.ToYAML()
	for _, secret := range []string{"path-secret", "append-secret", "probe-secret"} {
		if strings.Contains(y, secret) {
			t.Errorf("ToYAML leaked %q", secret)
		}
	}

	// the empty Authorization opt-out is not a credential and survives export
	o.HealthCheck.Headers = map[string]string{"authorization": ""}
	if v, ok := o.CloneYAMLSafe().HealthCheck.Headers["authorization"]; !ok || v != "" {
		t.Errorf("empty opt-out must be preserved, got %q ok=%t", v, ok)
	}
}

func TestCloneYAMLSafeMasksOriginCredentials(t *testing.T) {
	o := New()
	o.OriginURL = "mysql://origin:origin-secret@example.com/database"
	got := o.CloneYAMLSafe()
	if strings.Contains(got.OriginURL, "origin-secret") {
		t.Fatalf("CloneYAMLSafe exposed MySQL credentials: %+v", got)
	}
	if !strings.Contains(o.OriginURL, "origin-secret") {
		t.Fatal("CloneYAMLSafe mutated the source options")
	}
}

func TestToYAML(t *testing.T) {
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	s := o.ToYAML()
	if !(strings.Index(s, `provider: test_type`) > 0) {
		t.Error("ToYAML mismatch", s)
	}
}
