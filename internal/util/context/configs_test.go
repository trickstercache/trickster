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

package context

import (
	"context"
	"testing"

	cr "github.com/Comcast/trickster/internal/cache/registration"
	"github.com/Comcast/trickster/internal/config"
	"github.com/Comcast/trickster/internal/util/metrics"
)

func TestWithConfigs(t *testing.T) {

	metrics.Init()
	ctx := context.Background()

	// test empty reponses
	pc := PathConfig(ctx)
	if pc != nil {
		t.Errorf("expected nil path config, got one named %s", pc.Path)
	}

	oc := OriginConfig(ctx)
	if oc != nil {
		t.Errorf("expected nil origin config, got one named %s", oc.Name)
	}

	cc := CachingConfig(ctx)
	if cc != nil {
		t.Errorf("expected nil cache config, got one named %s", cc.CacheType)
	}

	co := CacheClient(ctx)
	if co != nil {
		t.Errorf("expected nil cache client, got one named %s", co.Configuration().CacheType)
	}

	oc = config.NewOriginConfig()
	cc = config.NewCacheConfig()
	pc = config.NewPathConfig()
	co = cr.NewCache("testing", cc)

	oc.Name = "testing"
	pc.Path = "/test/path"
	cc.Name = "testing"

	ctx = WithConfigs(ctx, oc, co, pc)

	pc = PathConfig(ctx)
	if pc == nil || pc.Path != "/test/path" {
		t.Errorf("expected path config response named %s", "/test/path")
	}

	oc = OriginConfig(ctx)
	if oc == nil || oc.Name != "testing" {
		t.Errorf("expected origin config response named %s", "testing")
	}

	cc = CachingConfig(ctx)
	if cc == nil || cc.Name != "testing" {
		t.Errorf("expected caching config response named %s", "testing")
	}

	co = CacheClient(ctx)
	if co == nil || co.Configuration().Name != "testing" {
		t.Errorf("expected cache client response named %s", "testing")
	}

}
