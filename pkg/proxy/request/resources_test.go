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
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	tc "github.com/trickstercache/trickster/v2/pkg/proxy/context"
)

func TestNewAndCloneResources(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r := NewResources(nil, nil, nil, nil, nil, nil)
	r.AlternateCacheTTL = time.Duration(1) * time.Second
	r2 := r.Clone()
	if r2.AlternateCacheTTL != r.AlternateCacheTTL {
		t.Errorf("expected %s got %s", r.AlternateCacheTTL.String(), r2.AlternateCacheTTL.String())
	}
}

func TestGetAndSetResources(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r := GetResources(nil)
	if r != nil {
		t.Error("expected nil reference")
	}

	r = NewResources(nil, nil, nil, nil, nil, nil)
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

func TestClone(t *testing.T) {
	t.Run("nil request", func(t *testing.T) {
		out, err := Clone(nil)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		if out != nil {
			t.Fatal("expected nil, got non-nil request")
		}
	})

	t.Run("request without resources", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		out, err := Clone(r)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		if out == r {
			t.Fatal("Clone should return a different request pointer")
		}
		if GetResources(out) != nil {
			t.Error("expected nil resources on clone of request without resources")
		}
	})

	t.Run("cloned resources are independent", func(t *testing.T) {
		rsc := NewResources(nil, nil, nil, nil, nil, nil)
		rsc.AlternateCacheTTL = time.Minute
		rsc.RequestBody = []byte("original")
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		r = SetResources(r, rsc)

		out, err := Clone(r)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		rsc2 := GetResources(out)
		if rsc2 == nil {
			t.Fatal("expected resources on cloned request")
		}
		if rsc2 == rsc {
			t.Fatal("Clone should return independent Resources pointer")
		}
		if rsc2.AlternateCacheTTL != time.Minute {
			t.Error("cloned resources should preserve AlternateCacheTTL")
		}
		// mutating the clone must not affect the original
		rsc2.AlternateCacheTTL = time.Hour
		rsc2.RequestBody[0] = 'X'
		if rsc.AlternateCacheTTL != time.Minute {
			t.Error("mutating clone affected original AlternateCacheTTL")
		}
		if rsc.RequestBody[0] != 'o' {
			t.Error("mutating clone affected original RequestBody")
		}
	})

	t.Run("POST body is cloned", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
		SetBody(r, []byte("post-body"))
		r.Body = io.NopCloser(strings.NewReader("post-body"))
		out, err := Clone(r)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		b, _ := io.ReadAll(out.Body)
		if string(b) != "post-body" {
			t.Errorf("expected 'post-body' got %q", string(b))
		}
	})
}

func TestCloneBare(t *testing.T) {
	t.Run("nil request", func(t *testing.T) {
		out, err := CloneWithoutResources(nil)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		if out != nil {
			t.Fatal("expected nil, got non-nil request")
		}
	})

	t.Run("strips resources", func(t *testing.T) {
		rsc := NewResources(nil, nil, nil, nil, nil, nil)
		rsc.AlternateCacheTTL = time.Minute
		r, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		r = SetResources(r, rsc)

		out, err := CloneWithoutResources(r)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		if GetResources(out) != nil {
			t.Error("CloneBare should not carry Resources")
		}
	})

	t.Run("POST body is cloned", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/", nil)
		SetBody(r, []byte("post-body"))
		r.Body = io.NopCloser(strings.NewReader("post-body"))
		out, err := CloneWithoutResources(r)
		if err != nil {
			t.Fatal("unexpected error:", err)
		}
		b, _ := io.ReadAll(out.Body)
		if string(b) != "post-body" {
			t.Errorf("expected 'post-body' got %q", string(b))
		}
	})
}

func TestMergeResources(t *testing.T) {
	logger.SetLogger(logging.ConsoleLogger(level.Error))
	r1 := NewResources(nil, nil, nil, nil, nil, nil)
	r1.AlternateCacheTTL = time.Minute
	r1.Merge(nil)
	if r1.AlternateCacheTTL != time.Minute {
		t.Errorf("nil merge shouldn't set anything in subject resources")
	}
	r2 := NewResources(nil, nil, nil, nil, nil, nil)
	r1.Merge(r2)
	if r1.AlternateCacheTTL != 0 {
		t.Errorf("merge should override subject resources")
	}
}
