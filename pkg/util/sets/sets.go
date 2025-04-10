package sets

import (
	"maps"
	"slices"
)

// Set is a collection of unique elements
type Set[T comparable] map[T]struct{}

// New creates a new Set from a slice of keys.
func New[T comparable](keys []T) Set[T] {
	s := make(Set[T], len(keys))
	for _, key := range keys {
		s[key] = struct{}{}
	}
	return s
}

// NewIntSet returns a new Set[int]
func NewIntSet() Set[int] {
	return make(Set[int])
}

// NewIntSet returns a new Set[string]
func NewStringSet() Set[string] {
	return make(Set[string])
}

// Add inserts a value into the set.
func (s Set[T]) Add(val T) {
	s[val] = struct{}{}
}

// Remove deletes a value from the set.
func (s Set[T]) Remove(val T) {
	delete(s, val)
}

// Contains checks if a value is in the set.
func (s Set[T]) Contains(val T) bool {
	_, ok := s[val]
	return ok
}

// Keys returns the set elements as a slice in an unpredictable order.
func (s Set[T]) Keys() []T {
	out := make([]T, len(s))
	var i int
	for key := range s {
		out[i] = key
		i++
	}
	return out
}

// Sorted returns the set elements as a sorted slice.
func (s Set[T]) Sorted(less func(a, b T) int) []T {
	out := s.Keys()
	slices.SortFunc(out, less)
	return out
}

// Clone returns a new independent copy of the set.
func (s Set[T]) Clone() Set[T] {
	return maps.Clone(s)
}

// Merge adds all elements from another set into this one.
func (s Set[T]) Merge(other Set[T]) {
	maps.Copy(s, other)
}
