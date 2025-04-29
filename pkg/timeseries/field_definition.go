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

package timeseries

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Field Data Types
const (
	Unknown FieldDataType = iota
	Int64
	Float64
	String
	Bool
	Byte
	Int16
)

// FieldDataType is a byte representing the data type of a Field
// when stored in a Point's Values list
type FieldDataType byte

// FieldDefinition describes a field by name and type
type FieldDefinition struct {
	Name           string        `msg:"name" json:"name"`
	DataType       FieldDataType `msg:"type" json:"type"`
	OutputPosition int           `msg:"pos" json:"pos,omitempty"`
	SDataType      string        `msg:"stype" json:"stype,omitempty"`
	ProviderData1  int           `msg:"provider1" json:"provider1,omitempty"`
	ProviderData2  int           `msg:"provider2" json:"provider2,omitempty"`
}

// FieldDefinitions represents a list type FieldDefinition
type FieldDefinitions []FieldDefinition

// Size returns the size of the FieldDefintions in bytes
func (fd FieldDefinition) Size() int {
	return 32 + len(fd.Name) + len(fd.SDataType) + 1 + 24 // string header size, string size, byte size, int size
}

func (fd FieldDefinition) String() string {
	b, err := json.Marshal(fd)
	if err != nil {
		return fmt.Sprintf(`{"error": "%s"}`, err)
	}
	return string(b)
}

func (fds FieldDefinitions) String() string {
	l := len(fds)
	if l == 0 {
		return "[]"
	}
	s := make([]string, l)
	for i, fd := range fds {
		s[i] = fd.String()
	}
	return "[" + strings.Join(s, ", ") + "]"

}
