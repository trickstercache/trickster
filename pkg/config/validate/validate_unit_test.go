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
	stderrors "errors"
	"path/filepath"
	"strings"
	"testing"

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rule "github.com/trickstercache/trickster/v2/pkg/backends/rule/options"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	alo "github.com/trickstercache/trickster/v2/pkg/observability/logging/accesslog/options"
	logmanager "github.com/trickstercache/trickster/v2/pkg/observability/logging/manager"
	lo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	mo "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
	auth "github.com/trickstercache/trickster/v2/pkg/proxy/authenticator/options"
	rwopts "github.com/trickstercache/trickster/v2/pkg/proxy/request/rewriter/options"
	tlsopts "github.com/trickstercache/trickster/v2/pkg/proxy/tls/options"
	tlstest "github.com/trickstercache/trickster/v2/pkg/testutil/tls"
)

func TestValidateNilConfig(t *testing.T) {
	t.Parallel()

	if err := Validate(nil); err != errors.ErrInvalidOptions {
		t.Fatalf("Validate(nil) = %v, want ErrInvalidOptions", err)
	}
}

func TestLoggingFilesRejectsConflictingOptions(t *testing.T) {
	name := filepath.Join(t.TempDir(), "shared.log")
	c := config.NewConfig()
	c.Logging.LogFile = name
	c.Backends = bo.Lookup{
		"one": {AccessLog: &alo.Options{Filename: name}},
	}
	if err := LoggingFiles(c); err != nil {
		t.Fatalf("matching options failed validation: %v", err)
	}
	compress := false
	c.Backends["one"].AccessLog.Compress = &compress
	if err := LoggingFiles(c); !stderrors.Is(err, logmanager.ErrConflictingOptions) {
		t.Fatalf("expected conflicting options error, got %v", err)
	}
}

func TestValidateSubsectionsNilOrEmpty(t *testing.T) {
	t.Parallel()

	if err := Rewriters(nil); err != nil {
		t.Fatalf("Rewriters(nil) = %v", err)
	}
	if err := Rules(nil); err != nil {
		t.Fatalf("Rules(nil) = %v", err)
	}
	if err := Caches(nil); err != nil {
		t.Fatalf("Caches(nil) = %v", err)
	}
	if err := Tracers(nil); err != nil {
		t.Fatalf("Tracers(nil) = %v", err)
	}
	if err := Authenticators(nil); err != nil {
		t.Fatalf("Authenticators(nil) = %v", err)
	}
	if err := NegativeCaches(nil); err != nil {
		t.Fatalf("NegativeCaches(nil) = %v", err)
	}
	if err := Backends(nil); err != errors.ErrNoValidBackends {
		t.Fatalf("Backends(nil) = %v, want ErrNoValidBackends", err)
	}
}

func TestValidateRejectsInvalidSubsections(t *testing.T) {
	t.Parallel()

	c := config.NewConfig()
	c.MgmtConfig.PprofListener = "invalid"
	if err := Validate(c); err == nil {
		t.Fatal("expected invalid mgmt config error")
	}

	c = config.NewConfig()
	c.Logging = &lo.Options{LogLevel: "trace"}
	if err := Validate(c); err == nil {
		t.Fatal("expected invalid log level error")
	}

	c = config.NewConfig()
	c.Metrics = &mo.Options{ListenPort: -1}
	if err := Validate(c); err == nil {
		t.Fatal("expected invalid metrics listen port error")
	}
}

func TestRewritersRulesAndAuthenticators(t *testing.T) {
	t.Parallel()

	c := config.NewConfig()
	c.RequestRewriters = rwopts.Lookup{
		"example": {Instructions: [][]string{{"header", "set", "X-Test", "1"}}},
	}
	if err := Rewriters(c); err != nil {
		t.Fatalf("Rewriters(valid) = %v", err)
	}
	c.RequestRewriters = rwopts.Lookup{"none": {}}
	if err := Rewriters(c); err == nil {
		t.Fatal("expected invalid rewriter name error")
	}

	c = config.NewConfig()
	c.Rules = rule.Lookup{
		"example": rule.New(),
	}
	if err := Rules(c); err != nil {
		t.Fatalf("Rules(valid) = %v", err)
	}
	c.Rules = rule.Lookup{"none": rule.New()}
	if err := Rules(c); err == nil {
		t.Fatal("expected invalid rule name error")
	}

	c = config.NewConfig()
	c.Authenticators = auth.Lookup{
		"example": {Provider: "basic"},
	}
	if err := Authenticators(c); err != nil {
		t.Fatalf("Authenticators(valid) = %v", err)
	}
	c.Authenticators = auth.Lookup{
		"example": {Provider: "not-a-provider"},
	}
	if err := Authenticators(c); err == nil {
		t.Fatal("expected invalid authenticator provider error")
	}
}

func TestTracersRejectsInvalidProtocol(t *testing.T) {
	t.Parallel()

	c := config.NewConfig()
	c.TracingOptions = to.Lookup{
		"default": {
			Provider: to.DefaultTracerProvider,
			Protocol: "udp",
		},
	}
	err := Tracers(c)
	if err == nil {
		t.Fatal("expected invalid tracing protocol error")
	}
	if !strings.Contains(err.Error(), "invalid tracing protocol [udp]") {
		t.Fatalf("Tracers(invalid protocol) = %v", err)
	}
}

func TestBackendsRequiresEntries(t *testing.T) {
	t.Parallel()

	c := config.NewConfig()
	c.Backends = bo.Lookup{}
	if err := Backends(c); err != errors.ErrNoValidBackends {
		t.Fatalf("Backends(empty) = %v", err)
	}

	c.Backends = bo.Lookup{
		"default": {
			Name:              "default",
			Provider:          providers.Prometheus,
			OriginURL:         "http://example.com:9090",
			CacheName:         "default",
			TracingConfigName: "",
			NegativeCacheName: "",
		},
	}
	c.Caches = co.Lookup{"default": co.New()}
	if err := Backends(c); err != nil {
		t.Fatalf("Backends(valid) = %v", err)
	}
}

func TestBackendsMarksFrontendServeTLS(t *testing.T) {
	t.Parallel()

	caFile := t.TempDir() + "/ca.pem"
	keyFile := t.TempDir() + "/key.pem"
	certFile := t.TempDir() + "/cert.pem"
	if err := tlstest.WriteTestKeyAndCert(true, "", caFile); err != nil {
		t.Fatal(err)
	}
	if err := tlstest.WriteTestKeyAndCert(false, keyFile, certFile); err != nil {
		t.Fatal(err)
	}

	c := config.NewConfig()
	c.Caches = co.Lookup{"default": co.New()}
	c.Backends = bo.Lookup{
		"default": {
			Name:              "default",
			Provider:          providers.Prometheus,
			OriginURL:         "http://example.com:9090",
			CacheName:         "default",
			TracingConfigName: "",
			NegativeCacheName: "",
			TLS: &tlsopts.Options{
				CertificateAuthorityPaths: []string{caFile},
				FullChainCertPath:         certFile,
				PrivateKeyPath:            keyFile,
			},
		},
	}
	if err := Backends(c); err != nil {
		t.Fatalf("Backends(tls) = %v", err)
	}
	if !c.Frontend.ServeTLS {
		t.Fatal("expected Frontend.ServeTLS to be set when backends present TLS certs")
	}
}

func TestValidateMinimalConfig(t *testing.T) {
	t.Parallel()

	c := config.NewConfig()
	c.Logging = &lo.Options{LogLevel: "info"}
	c.Metrics = &mo.Options{}
	c.Caches = co.Lookup{"default": co.New()}
	c.Backends = bo.Lookup{
		"default": {
			Name:              "default",
			Provider:          providers.Prometheus,
			OriginURL:         "http://example.com:9090",
			CacheName:         "default",
			TracingConfigName: "",
			NegativeCacheName: "",
		},
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate(minimal) = %v", err)
	}
}

func TestValidateLoadedConfig(t *testing.T) {
	t.Parallel()

	c, err := config.Load([]string{"-config", "../../../testdata/test.multiple_backends.conf"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("Validate(full config) = %v", err)
	}
}
