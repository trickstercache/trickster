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
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/checksum/fnv"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

// SeriesHeader is the header section of a series, and describes its
// shape, size, and attributes
type SeriesHeader struct {
	// Name is the name of the Series
	Name string `msg:"name"`
	// Tags is the map of tags associated with the Series
	Tags Tags `msg:"tags"`
	// FieldsList is the ordered list of fields in the Series
	FieldsList []timeseries.FieldDefinition `msg:"fields"`
	// TimestampIndex is the index of the TimeStamp field in the output when
	// it's time to serialize the DataSet for the wire
	TimestampIndex int `msg:"ti"`
	// QueryStatement is the original query to which this DataSet is associated
	QueryStatement string `msg:"query"`
	// Size is the memory utilization of the Header in bytes
	Size int `msg:"size"`

	hash Hash
}

// CalculateHash sums the FNV64a hash for the Header and stores it to the Hash member
func (sh *SeriesHeader) CalculateHash() Hash {
	if sh.hash > 0 {
		return sh.hash
	}
	hash := fnv.NewInlineFNV64a()
	hash.Write([]byte(sh.Name))
	hash.Write([]byte(sh.QueryStatement))
	for _, k := range sh.Tags.Keys() {
		hash.Write([]byte(k))
		hash.Write([]byte(sh.Tags[k]))
	}
	for _, fd := range sh.FieldsList {
		hash.Write([]byte(fd.Name))
		hash.Write([]byte{byte(fd.DataType)})
	}
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(sh.TimestampIndex))
	hash.Write(b)
	sh.hash = Hash(hash.Sum64())
	return sh.hash
}

// Clone returns a perfect, new copy of the SeriesHeader
func (sh *SeriesHeader) Clone() SeriesHeader {
	clone := SeriesHeader{
		Name:           sh.Name,
		Tags:           sh.Tags.Clone(),
		FieldsList:     make([]timeseries.FieldDefinition, len(sh.FieldsList)),
		TimestampIndex: sh.TimestampIndex,
		QueryStatement: sh.QueryStatement,
		Size:           sh.Size,
		hash:           sh.hash,
	}
	for i, fd := range sh.FieldsList {
		clone.FieldsList[i] = fd.Clone()
	}
	return clone
}

// CalculateSize sets and returns the header size
func (sh *SeriesHeader) CalculateSize() int {
	c := len(sh.Name) + sh.Tags.Size() + 8 + len(sh.QueryStatement) + 28
	for i := range sh.FieldsList {
		c += len(sh.FieldsList[i].Name) + 17
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
	if len(sh.FieldsList) > 0 {
		sb.WriteString(`"fields":[`)
		l := len(sh.FieldsList)
		for i, fd := range sh.FieldsList {
			fmt.Fprintf(sb, `"%s"`, fd.Name)
			if i < l-1 {
				sb.WriteByte(',')
			}
		}
		sb.WriteString("],")
	}
	sb.WriteString(`"timestampIndex":` + strconv.Itoa(sh.TimestampIndex))
	sb.WriteByte('}')
	return sb.String()
}
