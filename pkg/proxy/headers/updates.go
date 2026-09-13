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

package headers

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// UpdateOp is the operation a header update key spells with its first byte
type UpdateOp uint8

const (
	// UpdateSet replaces the header's value; it is the operation of a bare key
	UpdateSet UpdateOp = iota
	// UpdateAppend adds a value to the header; its key is prefixed with '+'
	UpdateAppend
	// UpdateDelete removes the header; its key is prefixed with '-'
	UpdateDelete
)

const (
	appendOperator = '+'
	deleteOperator = '-'
)

var (
	// ErrInvalidHeaderName indicates a header update names an invalid header
	ErrInvalidHeaderName = errors.New("invalid header name")
	// ErrInvalidHeaderValue indicates a header update carries an invalid value
	ErrInvalidHeaderValue = errors.New("invalid header value")
)

// ParseUpdateKey splits an update key into its operation and header name
func ParseUpdateKey(key string) (UpdateOp, string) {
	if len(key) > 0 {
		switch key[0] {
		case appendOperator:
			return UpdateAppend, key[1:]
		case deleteOperator:
			return UpdateDelete, key[1:]
		}
	}
	return UpdateSet, key
}

// UpdateKey spells an operation and header name as an update key
func UpdateKey(op UpdateOp, name string) string {
	switch op {
	case UpdateAppend:
		return string(appendOperator) + name
	case UpdateDelete:
		return string(deleteOperator) + name
	}
	return name
}

// ValidUpdate reports whether an update names a valid header and, unless it
// deletes the header, carries a valid value. A name beginning with an
// operator is rejected, since the key could not spell it unambiguously.
func ValidUpdate(op UpdateOp, name, value string) error {
	if name == "" || name[0] == appendOperator || name[0] == deleteOperator ||
		!httpguts.ValidHeaderFieldName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidHeaderName, name)
	}
	if op != UpdateDelete && !httpguts.ValidHeaderFieldValue(value) {
		return fmt.Errorf("%w for %q: %q", ErrInvalidHeaderValue, name, value)
	}
	return nil
}

type update struct {
	name  string
	op    UpdateOp
	value string
}

// Updates folds a sequence of header modifications into one operation per
// header name, in the order names were first modified. An update map is
// applied in no particular order, so it cannot say two things about one
// header; folding in declaration order keeps the last word.
type Updates struct {
	order []string
	ops   map[string]*update
}

func (u *Updates) at(name string) (*update, bool) {
	if u.ops == nil {
		u.ops = make(map[string]*update)
	}
	k := strings.ToLower(name)
	op, ok := u.ops[k]
	if !ok {
		op = &update{name: name}
		u.ops[k] = op
		u.order = append(u.order, k)
	}
	return op, ok
}

// Set replaces whatever the header would have held
func (u *Updates) Set(name, value string) {
	op, _ := u.at(name)
	op.op, op.value = UpdateSet, value
}

// Add appends a value: after a set or another add it joins that value, after
// a delete it is the value, and on its own it appends to the request's
func (u *Updates) Add(name, value string) {
	op, existed := u.at(name)
	switch {
	case !existed:
		op.op, op.value = UpdateAppend, value
	case op.op == UpdateDelete:
		op.op, op.value = UpdateSet, value
	default:
		op.value += "," + value
	}
}

// Remove deletes the header, whatever earlier modifications said
func (u *Updates) Remove(name string) {
	op, _ := u.at(name)
	op.op, op.value = UpdateDelete, ""
}

// Merge folds an update map whose keys carry the operator, in key order
func (u *Updates) Merge(m map[string]string) {
	for _, key := range slices.Sorted(maps.Keys(m)) {
		switch op, name := ParseUpdateKey(key); op {
		case UpdateDelete:
			u.Remove(name)
		case UpdateAppend:
			u.Add(name, m[key])
		default:
			u.Set(name, m[key])
		}
	}
}

// Len is the number of headers with a folded operation
func (u *Updates) Len() int {
	return len(u.order)
}

// Render emits the folded operations as an update map, or nil when empty
func (u *Updates) Render() map[string]string {
	if len(u.order) == 0 {
		return nil
	}
	out := make(map[string]string, len(u.order))
	for _, k := range u.order {
		op := u.ops[k]
		out[UpdateKey(op.op, op.name)] = op.value
	}
	return out
}
