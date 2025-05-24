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

package ranges_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/trickstercache/trickster/v2/pkg/proxy/ranges"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

func TestExtent(t *testing.T) {
	var e ranges.Extent[time.Time]
	e = timeseries.Extent{Start: time.Unix(100, 0), End: time.Unix(1000, 0)}
	require.True(t, e.StartsAt(time.Unix(100, 0)))

	t.Run("Extentlist", func(t *testing.T) {
		list := ranges.ExtentList[time.Time]{e}
		require.True(t, list.Encompasses(e))
	})

	// v2 wip
	var d ranges.Datumv2[time.Time, time.Duration]
	d = time.Time{}
	d = d.Add(time.Second)
	d = d.Add(time.Second)
	require.Equal(t, time.Time{}.Add(2*time.Second), d)

	var d2 ranges.Datumv2[ranges.Int64datumn, int64]
	d2 = ranges.Int64datumn(0)
	d2 = d2.Add(1)
	d2 = d2.Add(1)
}
