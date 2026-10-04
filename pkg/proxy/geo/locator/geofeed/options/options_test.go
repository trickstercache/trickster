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

package options

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/geofeed/feed"
	"github.com/trickstercache/trickster/v2/pkg/proxy/geo/locator/providers"

	"github.com/stretchr/testify/require"
)

func TestOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.csv")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	o := New()
	require.ErrorIs(t, o.Validate(), ErrNoEntries)
	o.Initialize()
	require.Equal(t, timeconv.Duration(providers.DefaultReloadInterval), o.ReloadInterval)

	o.Entries = []string{"192.0.2.0/24,US"}
	o.Files = []string{path}
	require.NoError(t, o.Validate())
	c := o.Clone()
	require.Equal(t, o, c)
	c.Entries[0] = "changed"
	require.Equal(t, "192.0.2.0/24,US", o.Entries[0])
	require.Nil(t, (*Options)(nil).Clone())

	o.Entries = []string{"192.0.2.0/24,UK"}
	require.ErrorIs(t, o.Validate(), feed.ErrInvalidLine)
	o.Entries = []string{"# a comment"}
	require.ErrorIs(t, o.Validate(), feed.ErrInvalidLine)
	o.Entries = nil
	o.Files = []string{path + ".missing"}
	require.ErrorIs(t, o.Validate(), os.ErrNotExist)
	o.Files = []string{path}
	o.ReloadInterval = timeconv.Duration(time.Second)
	require.ErrorIs(t, o.Validate(), providers.ErrInvalidReloadInterval)
}
