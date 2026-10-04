/*
 * Copyright 2026 The Trickster Authors
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

package providers

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProviders(t *testing.T) {
	for _, p := range []string{MMDB, Geofeed, Header} {
		require.True(t, IsValidProvider(p), p)
	}
	require.False(t, IsValidProvider("maxmind"))
	require.Equal(t, "geofeed, header, mmdb", Names())
	require.True(t, ReadsAddresses(MMDB))
	require.True(t, ReadsAddresses(Geofeed))
	require.False(t, ReadsAddresses(Header))
}

func TestValidateReloadInterval(t *testing.T) {
	for _, d := range []time.Duration{0, MinReloadInterval, time.Hour, MaxReloadInterval} {
		require.NoError(t, ValidateReloadInterval(d), d)
	}
	for _, d := range []time.Duration{time.Second, MaxReloadInterval + time.Second} {
		require.ErrorIs(t, ValidateReloadInterval(d), ErrInvalidReloadInterval, d)
	}
}

func TestCheckReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	require.ErrorIs(t, CheckReadable(path), os.ErrNotExist)
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.NoError(t, CheckReadable(path))
}
