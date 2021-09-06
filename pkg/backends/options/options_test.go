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
	"os"
	"strings"
	"testing"
	"time"

	ho "github.com/trickstercache/trickster/v2/pkg/backends/healthcheck/options"
	ro "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	"github.com/trickstercache/trickster/v2/pkg/cache/negative"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/headers"
	po "github.com/trickstercache/trickster/v2/pkg/proxy/paths/options"
	"github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
	"github.com/trickstercache/trickster/v2/pkg/util/yamlx"

	"gopkg.in/yaml.v2"
)

type testOptions struct {
	Backends map[string]*Options `yaml:"backends,omitempty"`
	ncl      negative.Lookups
}

func fromYAML(conf string) (*Options, error) {
	to := &testOptions{}

	err := yaml.Unmarshal([]byte(conf), to)
	if err != nil {
		return nil, err
	}

	md, err := yamlx.GetKeyList(conf)
	if err != nil {
		return nil, err
	}
	// always return the first one
	for k, o := range to.Backends {
		o.Name = k
		o.md = md
		return o, err
	}
	return nil, nil
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
	o.CompressibleTypes = map[string]interface{}{"test": nil}
	o.Paths = map[string]*po.Options{"test": p}
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

func testConfig() (Lookup, string) {
	n := New()
	n.Name = "test"
	n.Provider = "test"
	n.OriginURL = "http://1"
	ol := Lookup{"test": n}

	b, err := os.ReadFile("../../../testdata/test.empty.conf")
	if err != nil {
		panic(err)
	}
	return ol, string(b)
}

func TestValidateConfigMappings(t *testing.T) {

	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	ol := Lookup{o.Name: o}
	ol["frontend"] = o

	err = ol.ValidateConfigMappings(ro.Lookup{}, co.Lookup{})
	if err == nil {
		t.Error("expected error for invalid cache name")
	}

	// err = ol.ValidateConfigMappings(ro.Lookup{}, co.Lookup{"test": nil})
	// if err == nil {
	// 	t.Error("expected error for invalid backend name")
	// }

	delete(ol, "frontend")
	o.Provider = "rule"
	o.RuleName = "test"
	err = ol.ValidateConfigMappings(ro.Lookup{}, co.Lookup{"test": nil})
	if err == nil {
		t.Error("expected error for invalid rule name")
	}

	err = ol.ValidateConfigMappings(ro.Lookup{"test": new(ro.Options)}, co.Lookup{"test": nil})
	if err != nil {
		t.Error(err)
	}

	o.Name = ""
	err = ol.ValidateConfigMappings(ro.Lookup{"test": new(ro.Options)}, co.Lookup{"test": nil})
	if err == nil {
		t.Error("expected error for invalid backend name")
	}

	o.Name = "test"
	o.Provider = "alb"
	o.RuleName = ""
	err = ol.ValidateConfigMappings(ro.Lookup{"test": new(ro.Options)}, co.Lookup{"test": nil})
	if err != nil {
		t.Error(err)
	}

}

func testStringValueValidationError(to *testOptions, location *string, testValue string) error {
	// Test Invalid String
	s := *location
	*location = testValue
	err := Lookup(to.Backends).Validate(to.ncl)
	*location = s // restore original string
	return err
}

type intSwapper struct {
	location   *int
	restoreVal int
	testValue  int
}

func testIntegerValueValidationError(to *testOptions, sws []intSwapper) error {
	for i := range sws {
		sws[i].restoreVal = *sws[i].location
		*sws[i].location = sws[i].testValue
	}
	err := Lookup(to.Backends).Validate(to.ncl)
	for i := range sws {
		*sws[i].location = sws[i].restoreVal
	}
	return err
}

func TestValidate(t *testing.T) {

	ncl := testNegativeCaches()
	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}
	to := &testOptions{Backends: Lookup{o.Name: o}}
	to.ncl = ncl

	var errType01 = NewErrInvalidNegativeCacheName("invalid").(*ErrInvalidNegativeCacheName)
	var errType02 = NewErrMissingOriginURL("test").(*ErrMissingOriginURL)
	var errType03 = NewErrMissingProvider("test").(*ErrMissingProvider)

	// string value tests
	tests := []struct {
		to       *testOptions
		loc      *string
		val      string
		expected interface{}
	}{
		{ // 0 - invalid negative cache name
			to:       to,
			loc:      &o.NegativeCacheName,
			val:      "invalid",
			expected: errType01,
		},
		{ // 1 - valid negative cache name
			to:       to,
			loc:      &o.NegativeCacheName,
			val:      "test",
			expected: nil,
		},
		{ // 2 - invalid origin URL
			to:       to,
			loc:      &o.OriginURL,
			val:      "",
			expected: errType02,
		},
		{ // 3 - valid origin URL + strip trailing slash
			to:       to,
			loc:      &o.OriginURL,
			val:      "http://trickstercache.org/test/path/",
			expected: nil,
		},
		{ // 4 - invalid cache key prefix
			to:       to,
			loc:      &o.CacheKeyPrefix,
			val:      "",
			expected: nil,
		},
		{ // 5 - invalid provider
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

	// integer value tests
	tests2 := []struct {
		to       *testOptions
		sw       []intSwapper
		expected interface{}
	}{
		{ // case 0 - MaxShardSizeMS > 0 and MaxShardSizePoints > 0 are mutually exclusive
			to: to,
			sw: []intSwapper{
				{
					location:  &o.MaxShardSizeMS,
					testValue: 1,
				},
				{
					location:  &o.MaxShardSizePoints,
					testValue: 1,
				},
			},
			expected: ErrInvalidMaxShardSize,
		},
		{ // case 1 - verifies: if ShardStep > 0 && MaxShardSize == 0 { MaxShardSize = ShardStep }
			to: to,
			sw: []intSwapper{
				{
					location:  &o.ShardStepMS,
					testValue: 1,
				},
			},
			expected: nil,
		},
		{ // case 2 - verifies: if MaxShardSize % ShardStep != 0 { return ErrInvalidMaxShardSizeMS }
			to: to,
			sw: []intSwapper{
				{
					location:  &o.ShardStepMS,
					testValue: 10,
				},
				{
					location:  &o.MaxShardSizeMS,
					testValue: 32,
				},
			},
			expected: ErrInvalidMaxShardSizeMS,
		},
	}

	for i, test := range tests2 {
		t.Run(fmt.Sprintf("ints %d", i), func(t *testing.T) {
			err = testIntegerValueValidationError(test.to, test.sw)
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

}

func TestSetDefaults(t *testing.T) {

	o, err := fromTestYAML()
	if err != nil {
		t.Error(err)
	}

	backends := Lookup{o.Name: o}

	_, err = SetDefaults("test", o, o.md, nil, backends, map[string]interface{}{})
	if err != nil {
		t.Error(err)
	}

	_, err = SetDefaults("test", o, nil, nil, backends, map[string]interface{}{})
	if err != ErrInvalidMetadata {
		t.Error("expected invalid metadata, got", err)
	}

	o2, err := fromTestYAMLWithDefault()
	if err != nil {
		t.Error(err)
	}

	_, err = SetDefaults("test", o2, o2.md, nil, backends, map[string]interface{}{})
	if err != nil {
		t.Error(err)
	}

	o.Paths["series"].ReqRewriterName = "invalid"
	_, err = SetDefaults("test", o, o.md, nil, backends, map[string]interface{}{})
	if err == nil {
		t.Error("expected error for invalid rewriter name")
	}

	o2, err = fromTestYAMLWithReqRewriter()
	if err != nil {
		t.Error(err)
	}

	_, err = SetDefaults("test", o2, o2.md, map[string]rewriter.RewriteInstructions{"test": nil},
		backends, map[string]interface{}{})
	if err != nil {
		t.Error(err)
	}

	_, err = SetDefaults("test", o2, o2.md, map[string]rewriter.RewriteInstructions{"not-test": nil},
		backends, map[string]interface{}{})
	if err == nil {
		t.Error("expected error for invalid rewriter name")
	}

	o2, err = fromTestYAMLWithALB()
	if err != nil {
		t.Error(err)
	}

	_, err = SetDefaults("test", o2, o2.md, nil,
		backends, map[string]interface{}{})
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

	p, ok := o.Paths["series"]
	if !ok {
		t.Error("expected 'series' path")
	}
	p.RequestHeaders = map[string]string{headers.NameAuthorization: "trickster"}

	co := o.CloneYAMLSafe()
	p, ok = co.Paths["series"]
	if !ok {
		t.Error("expected 'series' path")
	}

	if v, ok := p.RequestHeaders[headers.NameAuthorization]; !ok || v != "*****" {
		t.Error("expected *****")
	}

	p.RequestHeaders = map[string]string{headers.NameAuthorization: "trickster"}

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
