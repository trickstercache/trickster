/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package registration

import (
	"errors"
	"testing"

	"github.com/Comcast/trickster/internal/config"
	tl "github.com/Comcast/trickster/internal/util/log"
)

func TestRegisterAll(t *testing.T) {

	// test nil config
	f, err := RegisterAll(nil, tl.ConsoleLogger("error"))
	if err == nil {
		t.Error(errors.New("expected error for no config provided"))
	}
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	// test good config
	f, err = RegisterAll(config.NewConfig(), tl.ConsoleLogger("error"))
	if err != nil {
		t.Error(err)
	}
	if len(f) != 1 {
		t.Errorf("expected %d got %d", 1, len(f))
	}

	// test bad implementation
	cfg := config.NewConfig()
	tc := cfg.Origins["default"].TracingConfig
	tc.Implementation = "foo"
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"))
	if err == nil {
		t.Error("expected error for invalid tracing implementation")
	}

	// test empty implementation
	tc.Implementation = ""
	f, _ = RegisterAll(cfg, tl.ConsoleLogger("error"))
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	// test nil tracing config
	cfg.Origins["default"].TracingConfig = nil
	f, _ = RegisterAll(cfg, tl.ConsoleLogger("error"))
	if len(f) > 0 {
		t.Errorf("expected %d got %d", 0, len(f))
	}

	// test nil origin config
	cfg.Origins = nil
	_, err = RegisterAll(cfg, tl.ConsoleLogger("error"))
	if err == nil {
		t.Error(errors.New("expected error for invalid tracing implementation"))
	}

}

func TestInit(t *testing.T) {
	tr, _, _ := Init(nil, tl.ConsoleLogger("error"))
	if tr == nil {
		t.Error("expected non-nil (noop) tracer")
	}
}
