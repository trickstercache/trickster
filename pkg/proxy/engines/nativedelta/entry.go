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

package nativedelta

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache"
	cacheproviders "github.com/trickstercache/trickster/v2/pkg/cache/providers"
	"github.com/trickstercache/trickster/v2/pkg/observability/keys"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/level"
	"github.com/trickstercache/trickster/v2/pkg/observability/logging/logger"
	"github.com/trickstercache/trickster/v2/pkg/timeseries"
)

const (
	envelopeVersion    byte = 1
	envelopeMarkerFlag byte = 1

	failureDecode    = "decode_failure"
	failureOversized = "oversized_cached_object"
	explicitRemoval  = "explicit_removal"
)

// envelopeMagic identifies a native-delta cache envelope: magic(4) version(1)
// flags(1) reserved(1) extentCount(4) extents(16 each) payloadLen(4) payload.
var envelopeMagic = [4]byte{'T', 'N', 'D', 'C'}

// Entry is one cached object: a protocol payload plus the extents it covers.
// A Marker entry carries no payload; its presence is its meaning (e.g. the
// unmergeable-plan fallback marker).
type Entry[R any] struct {
	Payload R
	Extents timeseries.ExtentList
	Marker  bool
	size    int
}

// Size reports the entry's approximate retained bytes, as last computed at
// store time.
func (e *Entry[R]) Size() int {
	if e == nil {
		return 0
	}
	return e.size
}

type tier[P any] struct {
	// one kind of entry, the object tier's protocol payloads or the delta tier's rows
	cfg   *Config
	codec Codec[P]
}

func (t *tier[P]) cacheClient() cache.Cache {
	if t.cfg.CacheClient == nil {
		return nil
	}
	return t.cfg.CacheClient()
}

func (t *tier[P]) observeCacheFailure(reason string) {
	if t.cfg.ObserveCacheFailure != nil {
		t.cfg.ObserveCacheFailure(reason)
	}
}

// Retrieve loads and validates the object-tier entry stored at key. Failures other than
// key-not-found are observed and the offending entry is removed.
func (e *Engine[R]) Retrieve(key string) (*Entry[R], bool) {
	return e.objects.retrieve(key)
}

// Store writes the object-tier entry at key: a typed reference in a memory cache, else an envelope.
// Failures are observed, not returned: a failed store costs only a later miss.
func (e *Engine[R]) Store(key string, entry *Entry[R]) {
	e.objects.store(key, entry, e.cfg.CacheTTL)
}

// RetrieveDelta loads and validates the delta-tier entry stored at key, as Retrieve does for
// the object tier.
func (e *Engine[R]) RetrieveDelta(key string) (*Entry[*Delta], bool) {
	return e.deltas.retrieve(key)
}

// StoreDelta writes the delta-tier entry at key, as Store does for the object tier.
func (e *Engine[R]) StoreDelta(key string, entry *Entry[*Delta]) {
	e.deltas.store(key, entry, e.cfg.CacheTTL)
}

// MarshalEntry encodes an object-tier entry into the versioned binary cache envelope.
func (e *Engine[R]) MarshalEntry(entry *Entry[R]) ([]byte, error) {
	return e.objects.marshalEntry(entry)
}

// UnmarshalEntry decodes an object-tier entry from a versioned binary cache envelope.
func (e *Engine[R]) UnmarshalEntry(data []byte) (*Entry[R], error) {
	return e.objects.unmarshalEntry(data)
}

func (t *tier[P]) retrieve(key string) (*Entry[P], bool) {
	cacheClient := t.cacheClient()
	if cacheClient == nil {
		return nil, false
	}
	if memoryCache, ok := memoryCacheClient(cacheClient); ok {
		value, _, err := memoryCache.RetrieveReference(key)
		if err != nil {
			if !errors.Is(err, cache.ErrKNF) {
				t.observeCacheFailure("retrieve_failure")
				t.logCacheError("native delta cache retrieval failed", err.Error())
			}
			return nil, false
		}
		entry, valid := value.(*Entry[P])
		if !valid || entry == nil {
			t.observeCacheFailure(failureDecode)
			t.remove(key, failureDecode)
			return nil, false
		}
		if t.cfg.MaxObjectSize > 0 && int64(entry.Size()) > t.cfg.MaxObjectSize {
			t.observeCacheFailure(failureOversized)
			t.remove(key, failureOversized)
			return nil, false
		}
		return entry, true
	}
	data, _, err := cacheClient.Retrieve(key)
	if err != nil {
		if !errors.Is(err, cache.ErrKNF) {
			t.observeCacheFailure("retrieve_failure")
			t.logCacheError("native delta cache retrieval failed", err.Error())
		}
		return nil, false
	}
	if t.cfg.MaxObjectSize > 0 && int64(len(data)) > t.cfg.MaxObjectSize {
		t.observeCacheFailure(failureOversized)
		t.remove(key, failureOversized)
		return nil, false
	}
	if c := cacheClient.Configuration(); c != nil && c.Provider == cacheproviders.BBolt {
		// codecs may keep referring to the bytes; bbolt's are only valid within its read transaction,
		// while every other provider's are a private copy or never change
		data = bytes.Clone(data)
	}
	entry, err := t.unmarshalEntry(data)
	if err != nil {
		t.observeCacheFailure(failureDecode)
		t.remove(key, failureDecode)
		return nil, false
	}
	return entry, true
}

func (t *tier[P]) store(key string, entry *Entry[P], ttl time.Duration) {
	if entry == nil {
		t.observeCacheFailure("encode_failure")
		t.logCacheError("native delta cache encoding failed", "nil cache entry")
		return
	}
	cacheClient := t.cacheClient()
	if cacheClient == nil {
		return
	}
	if memoryCache, ok := memoryCacheClient(cacheClient); ok {
		entry.size = t.entrySize(entry)
		if t.cfg.MaxObjectSize > 0 && int64(entry.size) > t.cfg.MaxObjectSize {
			t.observeCacheFailure("max_object_size")
			if logger.Level() == level.Debug {
				logger.Debug("native delta cache entry exceeds max object size",
					logging.Pairs{
						keys.Protocol:    t.cfg.Protocol,
						keys.BackendName: t.cfg.BackendName,
						keys.Size:        entry.size,
					})
			}
			return
		}
		if err := memoryCache.StoreReference(key, entry, ttl); err != nil {
			t.observeCacheFailure("store_failure")
			t.logCacheError("native delta cache storage failed", err.Error())
		}
		return
	}
	data, err := t.marshalEntry(entry)
	if err != nil {
		t.observeCacheFailure("encode_failure")
		t.logCacheError("native delta cache encoding failed", err.Error())
		return
	}
	if t.cfg.MaxObjectSize > 0 && int64(len(data)) > t.cfg.MaxObjectSize {
		t.observeCacheFailure("max_object_size")
		if logger.Level() == level.Debug {
			logger.Debug("native delta cache entry exceeds max object size",
				logging.Pairs{
					keys.Protocol:    t.cfg.Protocol,
					keys.BackendName: t.cfg.BackendName,
					keys.Size:        len(data),
				})
		}
		return
	}
	entry.size = len(data)
	if err := cacheClient.Store(key, data, ttl); err != nil {
		t.observeCacheFailure("store_failure")
		t.logCacheError("native delta cache storage failed", err.Error())
	}
}

func (t *tier[P]) remove(key, reason string) {
	cacheClient := t.cacheClient()
	if cacheClient == nil {
		return
	}
	if err := cacheClient.Remove(key); err != nil {
		t.observeCacheFailure("remove_failure")
		logger.Error("native delta cache removal failed",
			logging.Pairs{
				keys.Protocol:    t.cfg.Protocol,
				keys.BackendName: t.cfg.BackendName,
				keys.Reason:      reason,
				keys.Detail:      err.Error(),
			})
	}
}

func (t *tier[P]) entrySize(entry *Entry[P]) int {
	// approximates the heap retained by a typed memory-cache entry
	size := 64 + entry.Extents.Size()
	if !entry.Marker {
		size += t.codec.Size(entry.Payload)
	}
	if size < 0 {
		return math.MaxInt
	}
	return size
}

func (t *tier[P]) marshalEntry(entry *Entry[P]) ([]byte, error) {
	var payload []byte
	var flags byte
	if entry.Marker {
		flags |= envelopeMarkerFlag
	} else {
		var err error
		payload, err = t.codec.Marshal(entry.Payload)
		if err != nil {
			return nil, err
		}
	}
	if len(entry.Extents) > math.MaxUint32 || len(payload) > math.MaxUint32 {
		return nil, errors.New("native delta cache entry is too large to encode")
	}
	headerSize := 4 + 1 + 1 + 1 + 4 + len(entry.Extents)*16 + 4
	out := make([]byte, headerSize+len(payload))
	copy(out, envelopeMagic[:])
	out[4] = envelopeVersion
	out[5] = flags
	// #nosec G115 -- both lengths were bounded by math.MaxUint32 above.
	binary.BigEndian.PutUint32(out[7:11], uint32(len(entry.Extents)))
	position := 11
	for _, extent := range entry.Extents {
		// #nosec G115 -- the encoding deliberately preserves the signed Unix
		// nanosecond value's two's-complement bit pattern.
		binary.BigEndian.PutUint64(out[position:position+8], uint64(extent.Start.UnixNano()))
		// #nosec G115 -- see the corresponding signed decode below.
		binary.BigEndian.PutUint64(out[position+8:position+16], uint64(extent.End.UnixNano()))
		position += 16
	}
	// #nosec G115 -- both lengths were bounded by math.MaxUint32 above.
	binary.BigEndian.PutUint32(out[position:position+4], uint32(len(payload)))
	copy(out[position+4:], payload)
	return out, nil
}

func (t *tier[P]) unmarshalEntry(data []byte) (*Entry[P], error) {
	if len(data) < 15 || !slices.Equal(data[:4], envelopeMagic[:]) ||
		data[4] != envelopeVersion {
		return nil, errors.New("invalid native delta cache envelope")
	}
	flags := data[5]
	extentCount := int(binary.BigEndian.Uint32(data[7:11]))
	if extentCount > (len(data)-15)/16 {
		return nil, errors.New("invalid native delta cache extent count")
	}
	position := 11
	extents := make(timeseries.ExtentList, extentCount)
	for i := range extentCount {
		// #nosec G115 -- this reverses the intentional bit-preserving conversion
		// performed by marshalEntry.
		start := int64(binary.BigEndian.Uint64(data[position : position+8]))
		// #nosec G115 -- see the corresponding signed encode above.
		end := int64(binary.BigEndian.Uint64(data[position+8 : position+16]))
		extents[i] = timeseries.Extent{Start: time.Unix(0, start), End: time.Unix(0, end)}
		position += 16
	}
	if len(data)-position < 4 {
		return nil, errors.New("truncated native delta cache envelope")
	}
	payloadSize := int(binary.BigEndian.Uint32(data[position : position+4]))
	position += 4
	if payloadSize != len(data)-position {
		return nil, errors.New("invalid native delta cache payload size")
	}
	entry := &Entry[P]{Extents: extents, size: len(data)}
	if flags&envelopeMarkerFlag != 0 {
		entry.Marker = true
		return entry, nil
	}
	payload, err := t.codec.Unmarshal(data[position:])
	if err != nil {
		return nil, err
	}
	entry.Payload = payload
	return entry, nil
}

func memoryCacheClient(cacheClient cache.Cache) (cache.MemoryCache, bool) {
	if cacheClient == nil || cacheClient.Configuration() == nil ||
		cacheClient.Configuration().Provider != cacheproviders.Memory {
		return nil, false
	}
	if capability, ok := cacheClient.(interface{ SupportsReferences() bool }); ok &&
		!capability.SupportsReferences() {
		return nil, false
	}
	memoryCache, ok := cacheClient.(cache.MemoryCache)
	return memoryCache, ok
}

func (t *tier[P]) logCacheError(event, detail string) {
	logger.Error(event, logging.Pairs{
		keys.Protocol:    t.cfg.Protocol,
		keys.BackendName: t.cfg.BackendName,
		keys.Detail:      detail,
	})
}
