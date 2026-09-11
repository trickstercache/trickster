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

package model

import (
	"encoding/json"
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/proxy/response/merge"

	"github.com/stretchr/testify/require"
)

func TestMakeMergeFuncFromBytes(t *testing.T) {
	mergeFunc := MakeMergeFuncFromBytes("labels", func() *WFLabelData {
		return &WFLabelData{Envelope: &Envelope{}}
	})

	t.Run("success", func(t *testing.T) {
		accum := merge.NewAccumulator()
		body1, err := json.Marshal(&WFLabelData{
			Envelope: &Envelope{Status: "success"},
			Data:     []string{"a", "b"},
		})
		require.NoError(t, err)
		body2, err := json.Marshal(&WFLabelData{
			Envelope: &Envelope{Status: "success", Warnings: []string{"w1"}},
			Data:     []string{"c"},
		})
		require.NoError(t, err)

		require.NoError(t, mergeFunc(accum, body1, 0))
		require.NoError(t, mergeFunc(accum, body2, 1))

		got, ok := accum.GetGeneric().(*WFLabelData)
		require.True(t, ok)
		require.Equal(t, []string{"a", "b", "c"}, got.Data)
		require.Equal(t, "success", got.Status)
		require.Contains(t, got.Warnings, "w1")
	})

	t.Run("invalid_json", func(t *testing.T) {
		accum := merge.NewAccumulator()
		err := mergeFunc(accum, []byte(`{"status":`), 0)
		require.Error(t, err)
		require.Nil(t, accum.GetGeneric())
	})
}

func TestMakeMergeFuncUnexpectedType(t *testing.T) {
	mergeFunc := MakeMergeFunc("labels", func() *WFLabelData {
		return &WFLabelData{Envelope: &Envelope{}}
	})
	err := mergeFunc(merge.NewAccumulator(), 123, 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unexpected data type")
}
