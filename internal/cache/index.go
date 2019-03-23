/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package cache

import (
	"fmt"
	"sync"
	"time"
)

var indexLock = sync.Mutex{}

// Index maintains metadata about a Cache when Retention enforcement is managed internally,
// like memory or bbolt. It is not used for independently managed caches like Redis.
type Index struct {
	// CacheSize represents the size of the cache in bytes
	CacheSize int
	// ObjectCount represents the count of objects in the Cache
	ObjectCount int
	// Objects is a map of Objects in the Cache
	Objects map[string]*Object

	bulkRemoveFunc func([]string, bool) // do not store
	reapInterval   time.Duration        // do not store
	maxCacheSize   int                  // do not store
}

// NewIndex returns a new Index based on the provided inputs
func NewIndex(indexData []byte, bulkRemoveFunc func([]string, bool), reapInterval time.Duration) *Index {
	var i *Index
	if len(indexData) > 0 {
		//TODO: unmarshal the Index
		i.reapInterval = reapInterval
		i.bulkRemoveFunc = bulkRemoveFunc
	} else {
		i = &Index{reapInterval: reapInterval, bulkRemoveFunc: bulkRemoveFunc}
		i.Objects = make(map[string]*Object)
	}
	go i.Reap()
	return i
}

// UpdateObjectAccessTime updates the LastAccess for the object with the provided key
func (idx *Index) UpdateObjectAccessTime(key string) {
	indexLock.Lock()
	defer indexLock.Unlock()
	if _, ok := idx.Objects[key]; ok {
		idx.Objects[key].LastAccess = time.Now()
	}
}

// UpdateObject writes or updates the Index Metadata for the provided Object
func (idx *Index) UpdateObject(obj Object) {

	key := obj.Key
	if key == "" {
		return
	}

	indexLock.Lock()
	defer indexLock.Unlock()

	obj.Size = len(obj.Value)
	obj.Value = nil
	obj.LastAccess = time.Now()
	obj.LastWrite = obj.LastAccess

	if o, ok := idx.Objects[key]; ok {
		idx.CacheSize += (o.Size - idx.Objects[key].Size)
	} else {
		idx.CacheSize += obj.Size
		idx.ObjectCount++
	}

	idx.Objects[key] = &obj
}

// RemoveObject removes an Object's Metadata from the Index
func (idx *Index) RemoveObject(key string, noLock bool) {

	if !noLock {
		indexLock.Lock()
		defer indexLock.Unlock()
	}

	if o, ok := idx.Objects[key]; ok {
		idx.CacheSize -= o.Size
		idx.ObjectCount--
		delete(idx.Objects, key)
	}

}

// Reap continually iterates through the cache to find expired elements and removes them
func (idx *Index) Reap() {
	for {
		idx.ReapOnce()
		time.Sleep(idx.reapInterval)
		time.Sleep(time.Duration(5) * time.Second)
	}
}

// ReapOnce makes a single iteration through the cache to to find and remove expired elements
func (idx *Index) ReapOnce() {

	fmt.Println(idx)
	fmt.Println()

	indexLock.Lock()
	defer indexLock.Unlock()

	removals := make([]string, 0, 0)
	remainders := make([]*Object, 0, idx.ObjectCount)

	now := time.Now()

	for _, o := range idx.Objects {
		if o.Expiration.Before(now) {
			removals = append(removals, o.Key)
		} else {
			remainders = append(remainders, o)
		}
	}

	if len(removals) > 0 {
		fmt.Println("Found removals", removals)
		fmt.Println()
		idx.bulkRemoveFunc(removals, true)
	}

	if idx.CacheSize > idx.maxCacheSize {
		// Sort Remainders by LastAccess
		// Remove until idx.CacheSize is back under the threshold
	}

}
