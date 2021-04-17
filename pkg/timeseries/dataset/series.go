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

//go:generate msgp

package dataset

import (
	"fmt"
	"strings"
)

// Series represents a single timeseries in a Result
type Series struct {
	// Header is the Series Header describing the Series
	Header SeriesHeader `msg:"header"`
	// Points is the list of Points in the Series
	Points Points `msg:"points"`
	// PointSize is the memory utilization of the Points in bytes
	PointSize int64 `msg:"ps"`
}

// Hash is a numeric value representing a calculated hash
type Hash uint64

// Hashes is a slice of type Hash
type Hashes []Hash

// SeriesLookup is a map of Series searchable by Series Header Hash
type SeriesLookup map[SeriesLookupKey]*Series

// SeriesLookupKey is the key for a SeriesLookup, consisting of a Result.StatementID and a Series.Hash
type SeriesLookupKey struct {
	StatementID int
	Hash        Hash
}

// Size returns the memory utilization of the Series in bytes
func (s Series) Size() int64 {
	return int64(16 + s.PointSize + int64(s.Header.Size))
}

// Clone returns a perfect, new copy of the Series
func (s *Series) Clone() *Series {
	clone := &Series{Header: s.Header.Clone()}
	if s.Points != nil {
		clone.Points = s.Points.Clone()
	}
	return clone
}

func (s *Series) String() string {
	sb := strings.Builder{}
	sb.WriteString(`{"header":`)
	sb.WriteString(s.Header.String())
	sb.WriteString(`,points:[`)
	l := len(s.Points)
	for i, p := range s.Points {
		sb.WriteString(fmt.Sprintf(`{%d,`, p.Epoch))
		m := len(p.Values)
		for j, v := range p.Values {
			sb.WriteString(fmt.Sprintf(`%v`, v))
			if j < m-1 {
				sb.WriteByte(',')
			}
		}
		sb.WriteByte('}')
		if i < l-1 {
			sb.WriteByte(',')
		}
	}
	sb.WriteString(`]}`)
	return sb.String()
}
