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

//go:generate go tool msgp

package dataset

import (
	"fmt"
	"slices"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/checksum/fnv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SeriesHeader is the header section of a series, and describes its
// shape, size, and attributes
type SeriesHeader struct {
	// Name is the name of the Series
	Name string `msg:"name"`
	// Tags is the map of tags associated with the Series. Each key will map to
	// a fd.Name in TagFieldsList, with values representing the specific tag
	// values for this Series.
	Tags Tags `msg:"tags"`
	// TimestampField is the Field Definitions for the timestamp field.
	// Optional and used by some providers.
	TimestampField timeseries.FieldDefinition `msg:"timestampField"`
	// TagFieldsList is the ordered list of tag-based Field Definitions in the
	// Series. Optional and used by some providers.
	TagFieldsList timeseries.FieldDefinitions `msg:"tagFields"`
	// ValueFieldsList is the ordered list of value-based Field Definitions in the Series.
	ValueFieldsList timeseries.FieldDefinitions `msg:"valueFields"`
	// UntrackedFieldsList is alist of Field Definitions in the Series whose row values are ignored.
	UntrackedFieldsList timeseries.FieldDefinitions `msg:"untrackedFields"`
	// QueryStatement is the original query to which this DataSet is associated
	QueryStatement string `msg:"query"`
	// Size is the memory utilization of the Header in bytes
	Size int `msg:"size"`

	hash Hash
}

// seriesHeaderFNVHash is the shared FNV payload for CalculateHash; queryStatement
// is the string mixed into the hash in the same position as QueryStatement.
func seriesHeaderFNVHash(sh *SeriesHeader, queryStatement string) Hash {
	h := fnv.NewInlineFNV64a()
	h.Write([]byte(sh.Name))
	h.Write([]byte(queryStatement))
	for _, k := range sh.Tags.Keys() {
		h.Write([]byte(k))
		h.Write([]byte(sh.Tags[k]))
	}
	for _, fd := range sh.ValueFieldsList {
		h.Write([]byte(fd.Name))
		h.Write([]byte{byte(fd.DataType)})
	}
	for _, fd := range sh.UntrackedFieldsList {
		h.Write([]byte(fd.Name))
		h.Write([]byte{byte(fd.DataType)})
	}
	h.Write([]byte(sh.TimestampField.Name))
	h.Write([]byte{byte(sh.TimestampField.DataType)})
	return Hash(h.Sum64())
}

// CalculateHash sums the FNV64a hash for the Header and stores it to the Hash member
func (sh *SeriesHeader) CalculateHash(rehash ...bool) Hash {
	if (len(rehash) == 0 || !rehash[0]) && sh.hash > 0 {
		return sh.hash
	}
	sh.hash = seriesHeaderFNVHash(sh, sh.QueryStatement)
	return sh.hash
}

// CalculateHashWithQueryStatement returns the same hash CalculateHash would yield
// if SeriesHeader.QueryStatement were queryStatement. It does not read or update
// the header's cached hash, so callers can compare series across DataSets that
// intentionally use different stored statements (e.g. TSM sum/count rewrites of one
// logical avg query) without affecting normal CalculateHash behavior elsewhere.
func (sh *SeriesHeader) CalculateHashWithQueryStatement(queryStatement string) Hash {
	return seriesHeaderFNVHash(sh, queryStatement)
}

// Clone returns a perfect, new copy of the SeriesHeader
func (sh *SeriesHeader) Clone() SeriesHeader {
	clone := SeriesHeader{
		Name:                sh.Name,
		Tags:                sh.Tags.Clone(),
		ValueFieldsList:     make([]timeseries.FieldDefinition, len(sh.ValueFieldsList)),
		TagFieldsList:       make([]timeseries.FieldDefinition, len(sh.TagFieldsList)),
		UntrackedFieldsList: make([]timeseries.FieldDefinition, len(sh.UntrackedFieldsList)),
		TimestampField:      sh.TimestampField,
		QueryStatement:      sh.QueryStatement,
		Size:                sh.Size,
	}
	copy(clone.ValueFieldsList, sh.ValueFieldsList)
	copy(clone.TagFieldsList, sh.TagFieldsList)
	copy(clone.UntrackedFieldsList, sh.UntrackedFieldsList)
	return clone
}

// CalculateSize sets and returns the header size
func (sh *SeriesHeader) CalculateSize() int {
	// 16 is the string header size on 64-bit arch, while 8 is for sh.Size
	c := len(sh.Name) + 16 + sh.Tags.Size() + len(sh.QueryStatement) + 16 +
		sh.TimestampField.Size() + 8
	for i := range sh.ValueFieldsList {
		c += sh.ValueFieldsList[i].Size()
	}
	for i := range sh.UntrackedFieldsList {
		c += sh.UntrackedFieldsList[i].Size()
	}
	for i := range sh.TagFieldsList {
		c += sh.TagFieldsList[i].Size()
	}
	sh.Size = c
	return c
}

func (sh *SeriesHeader) String() string {
	sb := &strings.Builder{}
	sb.WriteByte('{')
	if sh.Name != "" {
		fmt.Fprintf(sb, `"name":"%s",`, sh.Name)
	}
	if sh.QueryStatement != "" {
		fmt.Fprintf(sb, `"query":"%s",`, sh.QueryStatement)
	}
	if len(sh.Tags) > 0 {
		fmt.Fprintf(sb, `"tags":"%s",`, sh.Tags.String())
	}

	// Helper function to write field lists to JSON
	writeFieldList := func(fieldName string, fields timeseries.FieldDefinitions) {
		if len(fields) > 0 {
			fmt.Fprintf(sb, `"%s":[`, fieldName)
			l := len(fields)
			for i, fd := range fields {
				fmt.Fprintf(sb, `"%s"`, fd.Name)
				if i < l-1 {
					sb.WriteByte(',')
				}
			}
			sb.WriteString("],")
		}
	}

	writeFieldList("valueFields", sh.ValueFieldsList)
	writeFieldList("tagFields", sh.TagFieldsList)
	writeFieldList("untrackedFields", sh.UntrackedFieldsList)
	fmt.Fprintf(sb, `"timeStampField":"%s"`, sh.TimestampField.Name)
	sb.WriteByte('}')
	return sb.String()
}

// FieldDefinitions returns all FieldDefinitions in the series ordered by OutputPosition
func (sh *SeriesHeader) FieldDefinitions() timeseries.FieldDefinitions {
	maxFields := len(sh.TagFieldsList) + len(sh.ValueFieldsList) +
		len(sh.UntrackedFieldsList) + 1 // +1 is for Timestamp field
	out := make(timeseries.FieldDefinitions, maxFields)
	var k int

	if sh.TimestampField.OutputPosition >= 0 && sh.TimestampField.OutputPosition < maxFields {
		out[k] = sh.TimestampField
		k++
	}

	for _, fd := range sh.TagFieldsList {
		if fd.OutputPosition >= 0 && fd.OutputPosition < maxFields {
			out[k] = fd
			k++
		}
	}
	for _, fd := range sh.ValueFieldsList {
		if fd.OutputPosition >= 0 && fd.OutputPosition < maxFields {
			out[k] = fd
			k++
		}
	}
	for _, fd := range sh.UntrackedFieldsList {
		if fd.OutputPosition >= 0 && fd.OutputPosition < maxFields {
			out[k] = fd
			k++
		}
	}
	out = out[:k]
	slices.SortFunc(out, func(a, b timeseries.FieldDefinition) int {
		return a.OutputPosition - b.OutputPosition
	})
	return out
}
