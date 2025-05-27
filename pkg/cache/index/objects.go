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

package index

import (
	"sync"
	"sync/atomic"

	"github.com/tinylib/msgp/msgp"
)

//go:generate go tool msgp

// Objects are the set of objects in the Index by cacheKey, represented as a map for encoding and decoding purposes
type Objects map[string]*Object

//msgp:ignore SyncObjects

// SyncObjects is a cache of Index Objects, backed by a sync.Map, that supports msgp encoding and decoding
type SyncObjects struct {
	sync.Map
	keys atomic.Int64
}

func (i *SyncObjects) Keys() []string {
	out := []string{}
	i.Range(func(k, _ any) bool {
		out = append(out, k.(string))
		return true
	})
	return out
}

func (i *SyncObjects) Store(key, value any) {
	i.Map.Store(key, value)
	i.keys.Add(1)
}

func (i *SyncObjects) Delete(key any) {
	i.Map.Delete(key)
	i.keys.Add(-1)
}

func (i *SyncObjects) FromObjects(in Objects) {
	for k, v := range in {
		i.Store(k, v)
	}
}

func (i *SyncObjects) ToObjects() Objects {
	out := make(Objects, i.keys.Load())
	i.Range(func(k, v any) bool {
		out[k.(string)] = v.(*Object)
		return true
	})
	return out
}

func (i *SyncObjects) EncodeMsg(en *msgp.Writer) (err error) {
	return i.ToObjects().EncodeMsg(en)
}

func (i *SyncObjects) DecodeMsg(dc *msgp.Reader) (err error) {
	objects := &Objects{}
	if err := objects.DecodeMsg(dc); err != nil {
		return err
	}
	i.FromObjects(*objects)
	return
}

func (i *SyncObjects) MarshalMsg(b []byte) (o []byte, err error) {
	return i.ToObjects().MarshalMsg(b)
}

func (i *SyncObjects) UnmarshalMsg(bts []byte) (o []byte, err error) {
	objects := &Objects{}
	o, err = objects.UnmarshalMsg(bts)
	if err != nil {
		return o, err
	}
	i.FromObjects(*objects)
	return
}

func (i *SyncObjects) Msgsize() (s int) {
	return i.ToObjects().Msgsize()
}
