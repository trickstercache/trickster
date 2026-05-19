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

package fanout

import (
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/pool"
	"github.com/trickstercache/trickster/v2/pkg/testutil/albpool"
)

func TestAllAllSlotsCloneError(t *testing.T) {
	run := func(t *testing.T, cfg Config) {
		const n = 4
		targets := make(pool.Targets, n)
		for i := range n {
			targets[i], _ = albpool.Target(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("handler should not run when clone fails")
				w.WriteHeader(http.StatusOK)
			}))
		}
		parent, err := http.NewRequest(http.MethodPost, "http://trickstercache.org/", errReader{})
		require.NoError(t, err)

		before := runtime.NumGoroutine()

		done := make(chan struct {
			res []Result
			err error
		}, 1)
		go func() {
			res, err := All(context.Background(), parent, targets, cfg)
			done <- struct {
				res []Result
				err error
			}{res, err}
		}()

		var out struct {
			res []Result
			err error
		}
		select {
		case out = <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("All did not return within 1s when every clone errors")
		}

		require.Error(t, out.err)
		require.Len(t, out.res, n)
		for i := range n {
			require.Equal(t, i, out.res[i].Index, "slot %d index mismatch", i)
			require.True(t, out.res[i].Failed, "slot %d should be Failed", i)
			require.Nil(t, out.res[i].Capture, "slot %d Capture should be nil", i)
			require.Error(t, out.res[i].Err, "slot %d Err should be non-nil", i)
		}

		time.Sleep(50 * time.Millisecond)
		after := runtime.NumGoroutine()
		require.LessOrEqual(t, after-before, 2, "goroutine leak suspected: before=%d after=%d", before, after)
	}

	t.Run("unlimited", func(t *testing.T) {
		run(t, Config{Mechanism: "test"})
	})

	t.Run("concurrency_limited", func(t *testing.T) {
		run(t, Config{Mechanism: "test", ConcurrencyLimit: 2})
	})
}
