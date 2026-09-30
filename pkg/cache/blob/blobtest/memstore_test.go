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

package blobtest

import (
	"errors"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob"

	"github.com/stretchr/testify/require"
)

func TestMemStore(t *testing.T) {
	RunStoreSuite(t, func(*testing.T) blob.Store { return NewMemStore() })
	RunReplacementSuite(t, func(*testing.T) blob.Store { return NewMemStore() })
	RunMetaStoreSuite(t, func(*testing.T) blob.MetaStore { return NewMemStore() })
}

func TestMemStoreFaults(t *testing.T) {
	errFault := errors.New("fault")
	s := NewMemStore()
	require.NoError(t, s.Connect())
	require.NoError(t, s.Close())
	require.False(t, s.Streamable())

	s.SetFrame("k", []byte("frame"))
	require.Equal(t, 1, s.Len())
	s.SetMeta("m", []byte("meta"))
	s.Free = 42
	free, err := s.FreeBytes()
	require.NoError(t, err)
	require.Equal(t, int64(42), free)

	s.ReadErr = errFault
	b, err := s.Open("k")
	require.NoError(t, err)
	_, err = b.ReadAt(make([]byte, 1), 0)
	require.ErrorIs(t, err, errFault)

	s.ReadErr, s.ReadErrAfter = nil, 1
	b, err = s.Open("k")
	require.NoError(t, err)
	_, err = b.ReadAt(make([]byte, 1), 0)
	require.NoError(t, err)
	_, err = b.ReadAt(make([]byte, 1), 0)
	require.ErrorIs(t, err, ErrReadAfter)

	s.PutErr, s.OpenErr, s.DeleteErr, s.ScanErr, s.MetaErr = errFault, errFault, errFault, errFault, errFault
	require.ErrorIs(t, s.Put("k", nil, nil, nil), errFault)
	_, err = s.Open("k")
	require.ErrorIs(t, err, errFault)
	require.ErrorIs(t, s.Delete("k"), errFault)
	_, _, err = s.Scan("", 1, func(blob.Blob) bool { return false })
	require.ErrorIs(t, err, errFault)
	_, err = s.CreateMeta("m")
	require.ErrorIs(t, err, errFault)
	require.ErrorIs(t, s.AppendMeta("m", nil), errFault)
	_, err = s.OpenMeta("m")
	require.ErrorIs(t, err, errFault)
	require.ErrorIs(t, s.RemoveMeta("m"), errFault)
	_, err = s.ListMeta()
	require.ErrorIs(t, err, errFault)
}
