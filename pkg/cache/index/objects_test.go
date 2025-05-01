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

package index

import (
	"bytes"
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tinylib/msgp/msgp"
)

func TestSyncObjects(t *testing.T) {
	t.Run("Objects Interop", func(t *testing.T) {
		// init from simple map
		orig := Objects{
			"foo": &Object{
				Key:   "foo",
				Value: []byte("foo-value"),
			},
			"bar": &Object{
				Key:   "bar",
				Value: []byte("bar-value"),
			},
		}
		var s SyncObjects
		s.FromObjects(orig)

		// ensure both key/values are as expected
		v, ok := s.Load("foo")
		require.True(t, ok)
		require.Equal(t, orig["foo"], v)
		v, ok = s.Load("bar")
		require.True(t, ok)
		require.Equal(t, orig["bar"], v)

		// convert back to map and ensure both key/values are as expected
		converted := s.ToObjects()
		require.Equal(t, orig, converted)
	})

	// expectedKeys := maps.Keys(orig)

	t.Run("msgp", func(t *testing.T) {
		// init from simple map
		orig := Objects{
			"foo": &Object{
				Key:   "foo",
				Value: []byte("foo-value"),
			},
			"bar": &Object{
				Key:   "bar",
				Value: []byte("bar-value"),
			},
		}
		// init from map
		var s SyncObjects
		s.FromObjects(orig)

		// encode/decode
		var buf bytes.Buffer
		require.NoError(t, msgp.Encode(&buf, &s))
		var s2 SyncObjects
		require.NoError(t, msgp.Decode(&buf, &s2))

		// compare keys
		require.Equal(t, slices.Sorted(maps.Keys(s.ToObjects())), slices.Sorted(maps.Keys(s2.ToObjects())))
		// compare values
		for k, v := range orig {
			v2, ok := s2.Load(k)
			require.True(t, ok)
			require.True(t, v2.(*Object).Equal(v))
		}

		// marshal/unmarshal
		b := []byte{}
		b, err := s.MarshalMsg(b)
		require.NoError(t, err)
		var s3 SyncObjects
		_, err = s3.UnmarshalMsg(b)
		require.NoError(t, err)
		require.Equal(t, slices.Sorted(maps.Keys(s.ToObjects())), slices.Sorted(maps.Keys(s3.ToObjects())))
		for k, v := range orig {
			v2, ok := s3.Load(k)
			require.True(t, ok)
			require.True(t, v2.(*Object).Equal(v))
		}
	})
}
