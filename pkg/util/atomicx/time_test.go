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

package atomicx

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAtomicTime(t *testing.T) {
	ts := time.Unix(0, 0)
	at := NewAtomicTime(ts)
	require.True(t, ts.Equal(at.LoadTime()), "expected %v, got %v", ts, at.LoadTime())
	// update the time and make sure it updates
	ts = time.Now()
	at.StoreTime(ts)
	require.True(t, ts.Equal(at.LoadTime()), "expected %v, got %v", ts, at.LoadTime())
	t.Run("stdlib", func(t *testing.T) {
		ts := time.Unix(1, 23)
		at := AtomicTime{}
		at.FromTime(StandardLibTime{ts})
		require.True(t, ts.Equal(at.LoadTime()), "expected %v, got %v", ts, at.LoadTime())
		ts2 := at.ToTime()
		require.True(t, ts.Equal(ts2.Time), "expected %v, got %v", ts, ts2)
	})
}
