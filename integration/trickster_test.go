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

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/util/pointers"
)

// Test Trickster capabilities common to all backends / caches / configurations.
func TestTrickster(t *testing.T) {
	t.Run("config not found", func(t *testing.T) {
		// Simple test to ensure trickster returns an error if its config is not found.
		ctx := context.Background()
		expected := expectedStartError{
			ErrorContains: pointers.New("open testdata/cfg-notfound.yaml: no such file or directory"),
		}
		startTrickster(t, ctx, expected, "-config", "testdata/cfg-notfound.yaml")
	})
	t.Run("start and stop", func(t *testing.T) {
		// Simple test to ensure that Trickster can start and be stopped within a test.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		defer cancel()
		started := make(chan struct{})
		go func() { // wait for trickster to start
			time.Sleep(5 * time.Second) // TODO: remove sleep & return explicit start signal
			checkTricksterMetrics(t, "localhost:8480")
			started <- struct{}{}
		}()
		go startTrickster(t, ctx, expectedStartError{}, "-config", "../docs/developer/environment/trickster-config/trickster.yaml")
		<-started
		t.Log("started...")
		metrics := checkTricksterMetrics(t, "localhost:8480")
		t.Log("Trickster metrics:", metrics)
	})

	t.Run("issues", func(t *testing.T) {
		// Test for specific reported issues
		t.Run("856: rule backend panics", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			defer cancel()
			go startTrickster(t, ctx, expectedStartError{}, "-config", "testdata/issue-856.yaml")
			var body string
			var header http.Header
			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				body, header = checkTrickster(t, "localhost:8500", "example/query", 200)
			}, 5*time.Second, 1*time.Second)
			t.Log("Response body:", body)
			t.Log("Response header:", header)
		})
	})

}
