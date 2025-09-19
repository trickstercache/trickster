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

// Package index defines the Trickster Cache Index
package index

import (
	"bytes"
	"sort"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	"github.com/trickstercache/trickster/v2/pkg/cache/index/options"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"
)

//go:generate go tool msgp

// IndexKey is the key under which the index will write itself to its associated cache
const IndexKey = "cache.index"

// Object contains metadata about an item in the Cache
type Object struct {
	// Key represents the name of the Object and is the
	// accessor in a hashed collection of Cache Objects
	Key string `msg:"key"`
	// Expiration represents the time that the Object expires from Cache
	Expiration atomicx.Time `msg:"expiration,extension"`
	// LastWrite is the time the object was last Written
	LastWrite atomicx.Time `msg:"lastwrite,extension"`
	// LastAccess is the time the object was last Accessed
	LastAccess atomicx.Time `msg:"lastaccess,extension"`
	// Size the size of the Object in bytes
	Size int64 `msg:"size"`
	// Value is the value of the Object stored in the Cache
	// It is used by Caches but not by the Index
	Value []byte `msg:"value,omitempty"`
	// DirectValue is an interface value for storing objects by reference to a memory cache
	// Since we'd never recover a memory cache index from memory on startup, no need to msgpk
	ReferenceValue cache.ReferenceObject `msg:"-"`
}

func (o *Object) Equal(other *Object) bool {
	return o.Key == other.Key &&
		o.Expiration.Load().Equal(other.Expiration.Load()) &&
		o.LastWrite.Load().Equal(other.LastWrite.Load()) &&
		o.LastAccess.Load().Equal(other.LastAccess.Load()) &&
		o.Size == other.Size &&
		((o.ReferenceValue != nil && o.ReferenceValue == other.ReferenceValue) || bytes.Equal(o.Value, other.Value))
}

// ToBytes returns a serialized byte slice representing the Object
func (o *Object) ToBytes() []byte {
	bytes, _ := o.MarshalMsg(nil)
	return bytes
}

// ObjectFromBytes returns a deserialized Cache Object from a serialized byte slice
func ObjectFromBytes(data []byte) (*Object, error) {
	o := &Object{}
	_, err := o.UnmarshalMsg(data)
	return o, err
}

func reap(cacheSize int64, objectCount int64, remainders objectsAtime, opts options.Options) (evictionType string, removals []string) {
	if len(remainders) == 0 ||
		((opts.MaxSizeBytes == 0 || cacheSize <= opts.MaxSizeBytes) &&
			(opts.MaxSizeObjects == 0 || objectCount <= opts.MaxSizeObjects)) {
		return // nothing to do
	}
	switch {
	case opts.MaxSizeBytes > 0 && cacheSize > opts.MaxSizeBytes:
		evictionType = "size_bytes"
	case opts.MaxSizeObjects > 0 && objectCount > opts.MaxSizeObjects:
		evictionType = "size_objects"
	default:
		return
	}

	logger.Debug(
		"max cache size reached. evicting least-recently-accessed records",
		logging.Pairs{
			"reason":         evictionType,
			"cacheSizeBytes": cacheSize, "maxSizeBytes": opts.MaxSizeBytes,
			"cacheSizeObjects": objectCount, "maxSizeObjects": opts.MaxSizeObjects,
		},
	)

	removals = make([]string, 0)

	sort.Sort(remainders)

	var i int
	j := len(remainders)

	if evictionType == "size_bytes" {
		bytesNeeded := (cacheSize - opts.MaxSizeBytes)
		if opts.MaxSizeBytes > opts.MaxSizeBackoffBytes {
			bytesNeeded += opts.MaxSizeBackoffBytes
		}
		bytesSelected := int64(0)
		for bytesSelected < bytesNeeded && i < j {
			removals = append(removals, remainders[i].Key)
			bytesSelected += remainders[i].Size
			i++
		}
	} else {
		objectsNeeded := (objectCount - opts.MaxSizeObjects)
		if opts.MaxSizeObjects > opts.MaxSizeBackoffObjects {
			objectsNeeded += opts.MaxSizeBackoffObjects
		}
		objectsSelected := int64(0)
		for objectsSelected < objectsNeeded && i < j {
			removals = append(removals, remainders[i].Key)
			objectsSelected++
			i++
		}
	}

	return
}
