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

package dspoints

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/dataset"

	"github.com/stretchr/testify/require"
)

func TestOfAndAt(t *testing.T) {
	wide := dataset.NewSeries(dataset.SeriesHeader{}, dataset.Points{{Epoch: 1, Values: []any{"a", int64(2)}}})
	narrow := dataset.NewSeries(dataset.SeriesHeader{}, dataset.Points{{Epoch: 2, Values: []any{1.5}}})
	s := dataset.NewSeriesOf(dataset.SeriesHeader{}, append(wide.Segments(), narrow.Segments()...))
	// every row is as wide as the first Segment's, a narrower one padded with nil
	require.Equal(t, dataset.Points{{Epoch: 1, Values: []any{"a", int64(2)}}, {Epoch: 2, Values: []any{1.5, nil}}}, Of(s))
	// a row by itself is as wide as its own Segment
	require.Equal(t, dataset.Point{Epoch: 2, Values: []any{1.5}}, At(s, 1))
	require.Panics(t, func() { At(s, 2) })
	require.Nil(t, Of(dataset.NewSeries(dataset.SeriesHeader{}, nil)))
	bare := dataset.NewSeries(dataset.SeriesHeader{}, dataset.Points{{Epoch: 3}})
	require.Equal(t, dataset.Points{{Epoch: 3}}, Of(bare))
	require.Equal(t, dataset.Point{Epoch: 3}, At(bare, 0))
}
