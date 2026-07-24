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

	bo "github.com/trickstercache/trickster/v2/pkg/backends/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	co "github.com/trickstercache/trickster/v2/pkg/cache/options"
	"github.com/trickstercache/trickster/v2/pkg/config"
	"github.com/trickstercache/trickster/v2/pkg/errors"
	lo "github.com/trickstercache/trickster/v2/pkg/observability/logging/options"
	mo "github.com/trickstercache/trickster/v2/pkg/observability/metrics/options"
	to "github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
)

func TestValidateNilConfig(t *testing.T) {
	t.Parallel()

	if err := Validate(nil); err != errors.ErrInvalidOptions {
		t.Fatalf("Validate(nil) = %v, want ErrInvalidOptions", err)
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
