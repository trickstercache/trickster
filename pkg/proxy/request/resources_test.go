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

package request

import (
	"context"
	"net/http"
	"testing"
	"time"

	tl "github.com/trickstercache/trickster/v2/pkg/observability/logging"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

func TestNewAndCloneResources(t *testing.T) {
	r := NewResources(nil, nil, nil, nil, nil, nil, tl.ConsoleLogger("error"))
	r.AlternateCacheTTL = time.Duration(1) * time.Second
	r2 := r.Clone()
	if r2.AlternateCacheTTL != r.AlternateCacheTTL {
		t.Errorf("expected %s got %s", r.AlternateCacheTTL.String(), r2.AlternateCacheTTL.String())
	}
}

func TestGetAndSetResources(t *testing.T) {

	r := GetResources(nil)
	if r != nil {
		t.Error("expected nil reference")
	}

	r = NewResources(nil, nil, nil, nil, nil, nil, tl.ConsoleLogger("error"))
	r.AlternateCacheTTL = time.Duration(1) * time.Second
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	ctx := context.Background()
	// test nil short circuit bail out
	req = SetResources(req.WithContext(ctx), nil)
	req = SetResources(req.WithContext(ctx), r)
	r2 := GetResources(req)
	if r2.AlternateCacheTTL != r.AlternateCacheTTL {
		t.Errorf("expected %s got %s", r.AlternateCacheTTL.String(), r2.AlternateCacheTTL.String())
	}

	req, _ = http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
	ctx = context.Background()
	req = req.WithContext(ctx)

	// set something other than a resource into the context to verify a get returns nil
	req = req.WithContext(tc.WithResources(req.Context(), req))

	r3 := GetResources(req)
	if r3 != nil {
		t.Errorf("expected nil result, got %v", r3)
	}

}

func TestMergeResources(t *testing.T) {
	r1 := NewResources(nil, nil, nil, nil, nil, nil, tl.ConsoleLogger("error"))
	r1.NoLock = true
	r1.Merge(nil)
	if !r1.NoLock {
		t.Errorf("nil merge shouldn't set anything in subject resources")
	}
	r2 := NewResources(nil, nil, nil, nil, nil, nil, tl.ConsoleLogger("error"))
	r1.Merge(r2)
	if r1.NoLock {
		t.Errorf("merge should override subject resources")
	}
}
