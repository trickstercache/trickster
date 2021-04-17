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

// ResultsLookup is a map of Results searchable by the Statement ID
type ResultsLookup map[int]*Result

// Result represents the results of a single query statement in the DataSet
type Result struct {
	// StatementID represents the ID of the statement for this result. This field may not be
	// used by all tsdb implementations
	StatementID int `msg:"statement_id"`
	// Error represents a statement-level error
	Error string `msg:"error"`
	// SeriesList is an ordered list of the Series in this result
	SeriesList []*Series `msg:"series"`
}

// Size returns the size of the Result in bytes
func (r *Result) Size() int64 {
	c := int64(8+(8*len(r.SeriesList))+len(r.Error)+16) + 12
	for _, s := range r.SeriesList {
		if s != nil {
			c += s.Size()
		}
	}
	return c
}

// Hashes returns the ordered list of Hashes for the SeriesList in the Result
func (r *Result) Hashes() Hashes {
	if len(r.SeriesList) == 0 {
		return nil
	}
	h := make(Hashes, len(r.SeriesList))
	for i := range r.SeriesList {
		h[i] = r.SeriesList[i].Header.CalculateHash()
	}
	return h
}

// Clone returns an exact copy of the Result
func (r *Result) Clone() *Result {
	clone := &Result{
		StatementID: r.StatementID,
		Error:       r.Error,
		SeriesList:  make([]*Series, len(r.SeriesList)),
	}
	for i, s := range r.SeriesList {
		if s == nil {
			continue
		}
		clone.SeriesList[i] = s.Clone()
	}
	return clone
}

func (r *Result) String() string {
	sb := strings.Builder{}
	sb.WriteByte('{')
	if r.Error != "" {
		sb.WriteString(fmt.Sprintf(`"error":"%s",`, r.Error))
	}
	sb.WriteString(fmt.Sprintf(`"statementID":%d,`, r.StatementID))
	sb.WriteString("series:[")
	l := len(r.SeriesList)
	for i, s := range r.SeriesList {
		sb.WriteString(s.String())
		if i < l-1 {
			sb.WriteByte(',')
		}
	}
	sb.WriteString("]}")
	return sb.String()
}
