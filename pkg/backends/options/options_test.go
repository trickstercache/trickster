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

	"github.com/stretchr/testify/require"
	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	tro "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	autho "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

type testOptions struct {
	Backends Lookup `yaml:"backends,omitempty"`
}

func fromYAML(conf string) (*Options, yamlx.KeyLookup, error) {
	to := &testOptions{}

	err := yaml.Unmarshal([]byte(conf), to)
	if err != nil {
		return nil, nil, err
	}

	md, err := yamlx.GetKeyList(conf)
	if err != nil {
		return nil, nil, err
	}
	// always return the first one
	for k, o := range to.Backends {
		o.Name = k
		return o, md, err
	}
	return nil, nil, nil
}

func TestNew(t *testing.T) {
	o := New()
	if o == nil {
		t.Error("expected non-nil options")
	}
}

func TestClone(t *testing.T) {
	p := po.New()
	o := New()
	o.Hosts = []string{"test"}
	o.CacheName = "test"
	o.CompressibleTypes = sets.New([]string{"test"})
	o.Paths = po.Lookup{"test": p}
	o.NegativeCache = map[int]time.Duration{1: 1}
	o.HealthCheck = &ho.Options{}
	o.FastForwardPath = p
	o.RuleOptions = &ro.Options{}
	o2 := o.Clone()
	if o2.CacheName != "test" {
		t.Error("clone failed")
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

	o, _, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	ol := Lookup{o.Name: o}
	ol["frontend"] = o

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
	location   *time.Duration
	restoreVal time.Duration
	testValue  time.Duration
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

	o, _, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	to := &testOptions{Backends: Lookup{o.Name: o}}

	var errType02 = NewErrMissingOriginURL("test").(*ErrMissingOriginURL)
	var errType03 = NewErrMissingProvider("test").(*ErrMissingProvider)

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
					testValue: 1,
				},
			},
			expected: nil,
		},
		{ // case 2 - verifies: if MaxShardSize % ShardStep != 0 { return ErrInvalidMaxShardSizeMS }
			to: to,
			sw: []durationSwapper{
				{
					location:  &o.MaxShardSizeTime,
					testValue: 10,
				},
				{
					location:  &o.ShardStep,
					testValue: 32,
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
		opts.MaxShardSizeTime = 1 * time.Millisecond
		opts.MaxShardSizePoints = 1
		to := &testOptions{Backends: Lookup{o.Name: &opts}}
		require.ErrorIs(t, Lookup(to.Backends).Validate(), ErrInvalidMaxShardSize)
	})
}

func TestOverlayYAMLData(t *testing.T) {

	o, md, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}

	backends := Lookup{o.Name: o}

	_, err = OverlayYAMLData("test", o, backends, sets.NewStringSet(), md)
	if err != nil {
		t.Error(err)
	}

	_, err = OverlayYAMLData("test", o, backends, sets.NewStringSet(), nil)
	if err != ErrInvalidMetadata {
		t.Error("expected invalid metadata, got", err)
	}

	o2, md, err := fromTestYAMLWithDefault()
	if err != nil {
		t.Error(err)
	}

	_, err = OverlayYAMLData("test", o2, backends, sets.NewStringSet(), md)
	if err != nil {
		t.Error(err)
	}

	o2, md, err = fromTestYAMLWithReqRewriter()
	if err != nil {
		t.Error(err)
	}

	_, err = OverlayYAMLData("test", o2, backends, sets.NewStringSet(), md)
	if err != nil {
		t.Error(err)
	}

	o2, md, err = fromTestYAMLWithALB()
	if err != nil {
		t.Error(err)
	}

	_, err = OverlayYAMLData("test", o2, backends, sets.NewStringSet(), md)
	if err != nil {
		t.Error(err)
	}

}

func TestValidateTLSConfigs(t *testing.T) {

	o, _, err := fromTestYAML()
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

	b, err = l.ValidateTLSConfigs()
	if err != nil {
		t.Error(err)
	}

	if !b {
		t.Error("expected true")
	}

}

func TestCloneYAMLSafe(t *testing.T) {

	o, _, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}

	p, ok := o.Paths["series"]
	if !ok {
		t.Error("expected 'series' path")
	}
	p.RequestHeaders = map[string]string{headers.NameAuthorization: "trickster"}

	o2 := o.CloneYAMLSafe()
	p, ok = o2.Paths["series"]
	if !ok {
		t.Error("expected 'series' path")
	}

	if v, ok := p.RequestHeaders[headers.NameAuthorization]; !ok || v != "*****" {
		t.Error("expected *****")
	}

	p.RequestHeaders = map[string]string{headers.NameAuthorization: "trickster"}

}

func TestToYAML(t *testing.T) {
	o, _, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	s := o.ToYAML()
	if !(strings.Index(s, `provider: test_type`) > 0) {
		t.Error("ToYAML mismatch", s)
	}
}
