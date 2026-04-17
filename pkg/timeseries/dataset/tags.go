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
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Tags is a key/value pair associated with a Series to scope the cardinality of the DataSet
type Tags map[string]string

// InjectTags injects the provided tags into all series in all results in the DataSet
// in an insert-or-update fashion
func (ds *DataSet) InjectTags(tags Tags) {
	var wg sync.WaitGroup
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil {
				continue
			}
			wg.Go(func() {
				if s.Header.Tags == nil {
					s.Header.Tags = tags.Clone()
				} else {
					s.Header.Tags.Merge(tags.Clone())
				}
			})
		}
	}
	wg.Wait()
}

// StripTags removes the specified tag keys from all series in all results in the DataSet
// and forces a hash recalculation so that series identity reflects the updated tags.
func (ds *DataSet) StripTags(keys []string) {
	if len(keys) == 0 {
		return
	}
	for _, r := range ds.Results {
		if r == nil {
			continue
		}
		for _, s := range r.SeriesList {
			if s == nil || len(s.Header.Tags) == 0 {
				continue
			}
			for _, k := range keys {
				delete(s.Header.Tags, k)
			}
			s.Header.CalculateHash(true) // force rehash with updated tags
		}
	}
}

// StringsWithSep returns a string representation of the Tags with the provided key/value separator
func (t Tags) StringsWithSep(sep1, sep2 string) string {
	if len(t) == 0 {
		return ""
	}
	pairs := make(sort.StringSlice, len(t))
	var i int
	for k, v := range t {
		pairs[i] = fmt.Sprintf("%s%s%s", k, sep1, v)
		i++
	}
	sort.Sort(pairs)
	return strings.Join(pairs, sep2)
}

// String returns a string representation of the Tags
func (t Tags) String() string {
	if len(t) == 0 {
		return ""
	}
	return t.StringsWithSep("=", ";")
}

// JSON returns a string representation of the Tags as a JSON object. Keys
// are emitted in sorted order. Keys and values are escaped via encoding/json
// since Prometheus label values are arbitrary UTF-8 (see data model spec)
// and the older string-concat form produced invalid JSON for any value
// containing `"` or `\`.
func (t Tags) JSON() string {
	if len(t) == 0 {
		return "{}"
	}
	keys := t.Keys()
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(t[k])
		sb.Write(kb)
		sb.WriteByte(':')
		sb.Write(vb)
	}
	sb.WriteByte('}')
	return sb.String()
}

// KVP returns a string representation of the Tags as "key"="value","key2"="value2"
func (t Tags) KVP() string {
	if len(t) == 0 {
		return ""
	}
	return `"` + t.StringsWithSep(`"="`, `","`) + `"`
}

// Keys returns a string-sorted list of the Tags's keys
func (t Tags) Keys() []string {
	if len(t) == 0 {
		return nil
	}
	return slices.Sorted(maps.Keys(t))
}

// Size returns the byte size of the Tags
func (t Tags) Size() int {
	s := 48 + (len(t) * 128)
	for k := range t {
		s += (len(k) + len(t[k])) + 32
	}
	return s
}

// Clone returns an exact copy of the Tags
func (t Tags) Clone() Tags {
	return maps.Clone(t)
}

// Merge merges the provided tags into the subject tags, replacing any duplicate tag names
func (t Tags) Merge(t2 Tags) {
	maps.Copy(t, t2)
}
