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

package wire

import (
	"bytes"
	"errors"
)

const (
	// MaxNamespaceFields is the most fields a Track Namespace may hold
	MaxNamespaceFields = 32
	// MaxFullTrackNameLen bounds the summed field and track name lengths of a Full Track Name
	MaxFullTrackNameLen = 4096
	// MaxReasonLen bounds a Reason Phrase
	MaxReasonLen = 1024
	// MaxURILen bounds a session URI in GOAWAY and a Redirect
	MaxURILen = 8192
)

var (
	// ErrEmptyNamespaceField is returned for a Track Namespace Field of zero length
	ErrEmptyNamespaceField = errors.New("moq: empty track namespace field")
	// ErrNamespaceTooLong is returned for a namespace or full track name over its limits
	ErrNamespaceTooLong = errors.New("moq: track namespace or name too long")
)

// Location identifies an Object by its Group and Object IDs
type Location struct {
	Group  uint64
	Object uint64
}

// Compare orders Locations by Group, then Object
func (l Location) Compare(o Location) int {
	switch {
	case l.Group < o.Group:
		return -1
	case l.Group > o.Group:
		return 1
	case l.Object < o.Object:
		return -1
	case l.Object > o.Object:
		return 1
	}
	return 0
}

// Less reports whether l precedes o
func (l Location) Less(o Location) bool {
	return l.Compare(o) < 0
}

// AppendLocation appends a Location as two varints
func AppendLocation(b []byte, l Location) []byte {
	return AppendVarint(AppendVarint(b, l.Group), l.Object)
}

// Location reads a Location
func (r *Reader) Location() Location {
	return Location{Group: r.Varint(), Object: r.Varint()}
}

// Namespace is an ordered tuple of Track Namespace Fields; fields compare by exact bytes
type Namespace [][]byte

// Len returns the summed length of the namespace's fields
func (ns Namespace) Len() int {
	var n int
	for _, f := range ns {
		n += len(f)
	}
	return n
}

// Equal reports whether two namespaces hold the same fields
func (ns Namespace) Equal(o Namespace) bool {
	if len(ns) != len(o) {
		return false
	}
	for i := range ns {
		if !bytes.Equal(ns[i], o[i]) {
			return false
		}
	}
	return true
}

// HasPrefix reports whether prefix matches the leading fields of ns, field by field
func (ns Namespace) HasPrefix(prefix Namespace) bool {
	if len(prefix) > len(ns) {
		return false
	}
	for i := range prefix {
		if !bytes.Equal(ns[i], prefix[i]) {
			return false
		}
	}
	return true
}

// Clone returns a deep copy, so the namespace outlives the buffer it was decoded from
func (ns Namespace) Clone() Namespace {
	if ns == nil {
		return nil
	}
	out := make(Namespace, len(ns))
	buf := make([]byte, ns.Len())
	for i, f := range ns {
		n := copy(buf, f)
		out[i], buf = buf[:n:n], buf[n:]
	}
	return out
}

// Validate checks ns, with name as its track name when it names a track, against the field count, field length
// and total length limits a receiver enforces
func (ns Namespace) Validate(name []byte) error {
	if len(ns) > MaxNamespaceFields {
		return ErrNamespaceTooLong
	}
	n := len(name)
	for _, f := range ns {
		if len(f) == 0 {
			return ErrEmptyNamespaceField
		}
		n += len(f)
	}
	if n > MaxFullTrackNameLen {
		return ErrNamespaceTooLong
	}
	return nil
}

// AppendNamespace appends a Track Namespace
func AppendNamespace(b []byte, ns Namespace) []byte {
	b = AppendVarint(b, uint64(len(ns)))
	for _, f := range ns {
		b = AppendBytes(b, f)
	}
	return b
}

// Namespace reads a Track Namespace, enforcing the field count, field length and total length limits
func (r *Reader) Namespace() Namespace {
	n := r.Count()
	if r.err != nil {
		return nil
	}
	if n > MaxNamespaceFields {
		r.Fail(ErrNamespaceTooLong)
		return nil
	}
	if n == 0 {
		return Namespace{}
	}
	ns := make(Namespace, n)
	var total uint64
	for i := range ns {
		f := r.LenBytes(MaxFullTrackNameLen)
		if r.err != nil {
			return nil
		}
		if len(f) == 0 {
			r.Fail(ErrEmptyNamespaceField)
			return nil
		}
		total += uint64(len(f))
		if total > MaxFullTrackNameLen {
			r.Fail(ErrNamespaceTooLong)
			return nil
		}
		ns[i] = f
	}
	return ns
}

// TrackName reads a length-prefixed Track Name, which may be empty; with its namespace it must not exceed
// MaxFullTrackNameLen bytes
func (r *Reader) TrackName(ns Namespace) []byte {
	name := r.LenBytes(MaxFullTrackNameLen)
	if r.err == nil && ns.Len()+len(name) > MaxFullTrackNameLen {
		r.Fail(ErrNamespaceTooLong)
		return nil
	}
	return name
}

// Reason reads a Reason Phrase, failing past MaxReasonLen bytes
func (r *Reader) Reason() string {
	return string(r.LenBytes(MaxReasonLen))
}

// URI reads a length-prefixed session URI, failing past MaxURILen bytes
func (r *Reader) URI() string {
	return string(r.LenBytes(MaxURILen))
}
