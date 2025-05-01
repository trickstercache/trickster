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

func TestTime(t *testing.T) {
	ts := time.Unix(0, 0)
	at := NewTime(ts)
	require.True(t, ts.Equal(at.Load()), "expected %v, got %v", ts, at.Load())
	// update the time and make sure it updates
	ts = time.Now()
	at.Store(ts)
	require.True(t, ts.Equal(at.Load()), "expected %v, got %v", ts, at.Load())
	// start from empty value
	at = &Time{}
	ts = time.Unix(1, 23)
	at.Store(ts)
	require.True(t, ts.Equal(at.Load()), "expected %v, got %v", ts, at.Load())
	ts2 := at.Load()
	require.True(t, ts.Equal(ts2), "expected %v, got %v", ts, ts2)
	// check zero value
	at.Store(time.Time{})
	require.True(t, at.Load().IsZero(), "expected %v, got %v", time.Time{}, at.Load())
	require.True(t, at.Load().IsZero())

	t.Run("msgp.extension", func(t *testing.T) {
		// init with zero value and marshal
		ts := time.Unix(0, 0)
		at := NewTime(ts)
		buf := make([]byte, 15)
		err := at.MarshalBinaryTo(buf)
		require.NoError(t, err)

		// init with now and marshal
		now := time.Now()
		at = NewTime(now)
		buf = make([]byte, 15)
		err = at.MarshalBinaryTo(buf)
		require.NoError(t, err)

		// unmarshal, then compare against originals
		at2 := &Time{}
		err = at2.UnmarshalBinary(buf)
		require.NoError(t, err)
		require.True(t, at2.Load().Equal(at.Load()), "expected %v, got %v", ts, at2.Load())
		require.True(t, at2.Load().Equal(now), "expected %v, got %v", now, at2.Load())
	})
}
