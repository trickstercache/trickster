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
	"sort"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/observability/logging"
	"github.com/trickstercache/trickster/v2/pkg/util/atomicx"
)

var testLogger = logging.ConsoleLogger("error")

func testBulkRemoveFunc(cacheKeys []string) {
}
func fakeFlusherFunc(string, []byte) {}

type testReferenceObject struct {
}

func (r *testReferenceObject) Size() int {
	return 1
}

func TestObjectFromBytes(t *testing.T) {

	obj := &Object{}
	b := obj.ToBytes()
	obj2, err := ObjectFromBytes(b)
	if err != nil {
		t.Error(err)
	}

	if obj2 == nil {
		t.Errorf("nil cache index")
	}

}

func TestSort(t *testing.T) {

	o := objectsAtime{
		&Object{
			Key:        "3",
			LastAccess: *atomicx.NewTime(time.Unix(3, 0)),
		},
		&Object{
			Key:        "1",
			LastAccess: *atomicx.NewTime(time.Unix(1, 0)),
		},
		&Object{
			Key:        "2",
			LastAccess: *atomicx.NewTime(time.Unix(2, 0)),
		},
	}
	sort.Sort(o)

	if o[0].Key != "1" {
		t.Errorf("expected %s got %s", "1", o[0].Key)
	}

	if o[1].Key != "2" {
		t.Errorf("expected %s got %s", "2", o[1].Key)
	}

	if o[2].Key != "3" {
		t.Errorf("expected %s got %s", "3", o[2].Key)
	}

}
