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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/daemon"
)

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// the expected error for Trickster's 'Start' to return
type expectedStartError struct {
	ErrorContains *string
	Error         *error
}

// start a trickster instance with the provided context (for cancellation), and any args to pass to the daemon.
func startTrickster(t *testing.T, ctx context.Context, expected expectedStartError, args ...string) {
	err := daemon.Start(ctx, args...)
	if expected.Error != nil {
		require.ErrorIs(t, err, *expected.Error)
	} else if expected.ErrorContains != nil {
		require.ErrorContains(t, err, *expected.ErrorContains)
	} else {
		require.NoError(t, err)
	}
}

// query for prometheus metrics from a Trickster server at the given address.
func checkTricksterMetrics(t *testing.T, address string) []string {
	url := "http://" + filepath.Join(address, "metrics")
	t.Log("Checking Trickster metrics at", url)
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected 200 OK from Trickster metrics endpoint")
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	lines := strings.Split(string(b), "\n")
	// Filter out comments and empty lines
	return slices.DeleteFunc(lines, func(s string) bool {
		if strings.HasPrefix(s, "#") || s == "" {
			return true
		}
		return false
	})
}
