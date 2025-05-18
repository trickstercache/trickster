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

	"github.com/trickstercache/trickster/v2/pkg/errors"
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
	Uint64
	DateTimeRFC3339
	DateTimeRFC3339Nano
	DateTimeUnixSecs
	DateTimeUnixMilli
	DateTimeUnixMicro
	DateTimeUnixNano
	DateSQL
	TimeSQL
	DateTimeSQL
	Null
)

const (
	RoleUnknown FieldRole = iota
	RoleTimestamp
	RoleTag
	RoleValue
	RoleUntracked
)

// FieldDataType is a byte representing the data type of a Field
// when stored in a Point's Values list
type FieldDataType byte

// FieldDataType is a byte representing the role of a Field (Value, Tag, etc)
type FieldRole byte

// FieldDefinition describes a field by name and type
type FieldDefinition struct {
	Name           string        `msg:"n" json:"name"`
	DataType       FieldDataType `msg:"t" json:"type"`
	SDataType      string        `msg:"s" json:"stype,omitempty"`
	OutputPosition int           `msg:"p" json:"pos,omitempty"`
	DefaultValue   string        `msg:"v,omitempty" json:"dv,omitempty"`
	Role           FieldRole     `msg:"r,omitempty" json:"role,omitempty"`
	ProviderData1  byte          `msg:"d,omitempty" json:"providerData1,omitempty"`
}

// FieldDefinitions represents a list type FieldDefinition
type FieldDefinitions []FieldDefinition

// FieldDefinitionLookup represents a map of FieldDefinitions keyed by name
type FieldDefinitionLookup map[string]FieldDefinition

// SeriesFields groups together a Series's Timestamp, Tags and Value Fields
type SeriesFields struct {
	Timestamp     FieldDefinition
	Tags          FieldDefinitions
	Values        FieldDefinitions
	Untracked     FieldDefinitions
	ResultNameCol int
}

// Size returns the size of the FieldDefintions in bytes
func (fd FieldDefinition) Size() int {
	return 32 + len(fd.Name) + len(fd.SDataType) + 1 + 24 // string header size, string size, byte size, int size
}

func (fd FieldDefinition) String() string {
	b, err := json.Marshal(fd)
	if err != nil {
		return errors.NewErrorBody(err)
	}
	return string(b)
}

func (fds FieldDefinitions) Clone() FieldDefinitions {
	out := make(FieldDefinitions, len(fds))
	copy(out, fds)
	return out
}

func (fds FieldDefinitions) ToLookup() FieldDefinitionLookup {
	out := make(FieldDefinitionLookup, len(fds))
	for _, fd := range fds {
		out[fd.Name] = fd
	}
	return out
}

func (fds FieldDefinitions) String() string {
	l := len(fds)
	if l == 0 {
		return "[]"
	}
	b, err := json.Marshal(fds)
	if err != nil {
		return errors.NewErrorBody(err)
	}
	return string(b)
}
