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

package merge

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries"

	"github.com/stretchr/testify/require"
)

func TestAccumulatorGenericAccessors(t *testing.T) {
	t.Parallel()

	accum := NewAccumulator()
	require.Nil(t, accum.GetGeneric())

	accum.SetGeneric("payload")
	require.Equal(t, "payload", accum.GetGeneric())

	accum.Lock()
	require.Equal(t, "payload", accum.GetGenericUnsafe())
	accum.SetGenericUnsafe("updated")
	require.Equal(t, "updated", accum.GetGenericUnsafe())
	accum.Unlock()

	require.Equal(t, "updated", accum.GetGeneric())
}

func TestAccumulatorUpdateTSData(t *testing.T) {
	t.Parallel()

	accum := NewAccumulator()
	ds1 := makeTestDataSet(0, "up", nil, []int64{100}, []string{"1"})
	ds2 := makeTestDataSet(0, "up", nil, []int64{200}, []string{"2"})

	var seen timeseries.Timeseries
	accum.UpdateTSData(func(cur timeseries.Timeseries) timeseries.Timeseries {
		seen = cur
		return ds1
	})
	require.Nil(t, seen)
	require.Same(t, ds1, accum.GetTSData())

	accum.UpdateTSData(func(cur timeseries.Timeseries) timeseries.Timeseries {
		seen = cur
		return ds2
	})
	require.Same(t, ds1, seen)
	require.Same(t, ds2, accum.GetTSData())
}
