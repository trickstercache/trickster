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

package dataset

import (
	"reflect"
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/timeseries/epoch"
)

// Point is one row of a series: its epoch and values. Series hold their rows by column, so a Point
// only builds a series or reads a row where speed doesn't matter.
type Point struct {
	Epoch  epoch.Epoch
	Values []any
}

// Points is a list of Points.
type Points []Point

// Clone returns a copy of the Point with its own Values slice.
func (p *Point) Clone() Point {
	return Point{Epoch: p.Epoch, Values: slices.Clone(p.Values)}
}

// PointsAreEqual reports whether p1 and p2 hold the same epoch and deeply equal values.
func PointsAreEqual(p1, p2 Point) bool {
	return p1.Epoch == p2.Epoch && reflect.DeepEqual(p1.Values, p2.Values)
}

// Equal reports whether both lists hold the same points.
func (p Points) Equal(p2 Points) bool {
	return slices.EqualFunc(p, p2, PointsAreEqual)
}

// Clone returns a copy of the Points, each with its own Values slice.
func (p Points) Clone() Points {
	if p == nil {
		return nil
	}
	out := make(Points, len(p))
	for i := range p {
		out[i] = p[i].Clone()
	}
	return out
}
