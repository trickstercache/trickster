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

package tsm

import (
	"errors"
	"net/http"
	"testing"

	alberr "github.com/trickstercache/trickster/v2/pkg/backends/alb/errors"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/options"
	"github.com/trickstercache/trickster/v2/pkg/backends/prometheus"
	"github.com/trickstercache/trickster/v2/pkg/backends/providers"
	rt "github.com/trickstercache/trickster/v2/pkg/backends/providers/registry/types"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestRegistryEntry(t *testing.T) {
	t.Parallel()
	entry := RegistryEntry()
	if entry.Name != Name || entry.ShortName != ShortName {
		t.Fatalf("RegistryEntry = %+v, want Name=%q ShortName=%q", entry, Name, ShortName)
	}
	if entry.New == nil {
		t.Fatal("RegistryEntry.New is nil")
	}
}

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("invalid output format", func(t *testing.T) {
		t.Parallel()
		_, err := New(&options.ALBConfigs{OutputFormat: "not-a-provider"}, nil)
		if !errors.Is(err, alberr.ErrInvalidTimeSeriesMergeProvider) {
			t.Fatalf("New() error = %v, want ErrInvalidTimeSeriesMergeProvider", err)
		}
	})

	t.Run("missing factory", func(t *testing.T) {
		t.Parallel()
		_, err := New(&options.ALBConfigs{OutputFormat: providers.Prometheus}, rt.Lookup{})
		if !errors.Is(err, alberr.ErrInvalidTimeSeriesMergeProvider) {
			t.Fatalf("New() error = %v, want ErrInvalidTimeSeriesMergeProvider", err)
		}
	})

	t.Run("valid prometheus provider", func(t *testing.T) {
		t.Parallel()
		m, err := New(
			&options.ALBConfigs{OutputFormat: providers.Prometheus},
			rt.Lookup{providers.Prometheus: prometheus.NewClient},
		)
		if err != nil {
			t.Fatalf("New() unexpected error: %v", err)
		}
		if m == nil {
			t.Fatal("New() returned nil mechanism")
		}
		if m.Name() != ShortName {
			t.Fatalf("Name() = %q, want %q", m.Name(), ShortName)
		}
	})
}

func TestHandlerStopPool(t *testing.T) {
	t.Parallel()

	h := &handler{}
	p, _, _ := albpool.NewHealthy([]http.Handler{http.NotFoundHandler()})
	defer p.Stop()
	h.SetPool(p)
	h.StopPool()
}
