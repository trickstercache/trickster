/*
 * Copyright 2018 Comcast Cable Communications Management, LLC
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

package times

import (
	"errors"
	"sort"
	"testing"
	"time"
)

func TestSortFloats(t *testing.T) {
	f := Times{time.Unix(2, 0), time.Unix(1, 0)}
	sort.Sort(f)
	if f[0] != time.Unix(1, 0) {
		t.Error(errors.New("sort failed"))
	}
}

func TestString(t *testing.T) {
	f := Times{time.Unix(2, 0), time.Unix(1, 0)}
	const expected = "[ 2, 1 ]"
	if f.String() != expected {
		t.Errorf("expected %s got %s", expected, f.String())
	}
}

func TestFromMap(t *testing.T) {
	m := map[time.Time]bool{
		time.Unix(2, 0): true,
	}
	f := FromMap(m)
	const expected = "[ 2 ]"
	if f.String() != expected {
		t.Errorf("expected %s got %s", expected, f.String())
	}
}
