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

package registration

import (
	"testing"

	"github.com/trickstercache/trickster/v2/cmd/trickster/config"
	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/tracing/options"
)

func TestRegisterAll(t *testing.T) {

	// test nil config
	f, err := RegisterAll(nil, tl.ConsoleLogger("error"), true)
	if err == nil {
		t.Error("expected error for no config provided")
	}
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	// test good config
	f, err = RegisterAll(config.NewConfig(), tl.ConsoleLogger("error"), true)
	if err != nil {
		t.Error(err)
	}
	if len(f) != 1 {
		t.Errorf("expected %d got %d", 1, len(f))
	}

	// test bad implementation
	cfg := config.NewConfig()
	tc := options.New()

	cfg.TracingConfigs = make(map[string]*options.Options)
	cfg.TracingConfigs["test"] = tc
	cfg.TracingConfigs["test3"] = tc
	cfg.Backends["default"].TracingConfigName = "test"

	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "jaeger"
	tc.CollectorURL = "http://example.com"
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), false)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "stdout"
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "zipkin"
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if err != nil {
		t.Error(err)
	}

	tc.Provider = "foo"

	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if err == nil {
		t.Error("expected error for invalid provider")
	}

	// test empty implementation
	tc.Provider = ""
	f, _ = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	tc.Provider = "none"
	cfg.Backends["default"].TracingConfigName = "test2"
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if err == nil {
		t.Error("expected error for invalid tracing config name")
	}
	cfg.Backends["default"].TracingConfigName = "test"

	temp := cfg.TracingConfigs
	cfg.TracingConfigs = nil
	// test nil tracing config
	f, _ = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}
	cfg.TracingConfigs = temp

	// test nil backend options
	cfg.Backends = nil
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"), true)
	if err == nil {
		t.Error("expected error for invalid tracing implementation")
	}

}

func TestGetTracer(t *testing.T) {
	tr, _ := GetTracer(nil, tl.ConsoleLogger("error"), true)
	if tr != nil {
		t.Error("expected nil tracer")
	}
}
