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
	"testing"

	"github.com/Comcast/trickster/internal/config"
	tl "github.com/Comcast/trickster/internal/util/log"
	"github.com/Comcast/trickster/internal/util/metrics"
)

var testCacheKey, testCacheName, testCacheType string

func init() {

	testCacheKey = "test-key"
	testCacheName = "test-cache"
	testCacheType = "test"

	metrics.Init(&config.TricksterConfig{}, tl.ConsoleLogger("error"))

}

func TestObserveCacheMiss(t *testing.T) {
	e1 := fmt.Errorf("value for key [%s] not in cache", testCacheKey)
	_, err := ObserveCacheMiss(testCacheKey, testCacheName, testCacheType)
	if err.Error() != e1.Error() {
		t.Error(err)
	}
}

// ObserveCacheDel records a cache deletion event
func TestObserveCacheDel(t *testing.T) {
	ObserveCacheDel(testCacheName, testCacheType, 0)
}

func TestCacheError(t *testing.T) {
	_, err := CacheError(testCacheKey, testCacheName, testCacheType, "%s")
	if err.Error() != testCacheKey {
		t.Errorf("expected %s got %s", testCacheKey, err.Error())
	}
}

func TestObserveCacheOperation(t *testing.T) {
	ObserveCacheOperation(testCacheName, testCacheType, "set", "ok", 0)
	ObserveCacheOperation(testCacheName, testCacheType, "set", "ok", 1)
}

func TestObserveCacheEvent(t *testing.T) {
	ObserveCacheEvent(testCacheName, testCacheType, "test", "test")
}

func TestObserveCacheSizeChange(t *testing.T) {
	ObserveCacheSizeChange(testCacheName, testCacheType, 0, 0)
}
