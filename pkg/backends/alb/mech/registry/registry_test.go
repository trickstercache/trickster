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

package registry

import (
	"testing"

	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/rr"
	"github.com/trickstercache/trickster/v2/pkg/backends/alb/mech/types"
	"github.com/trickstercache/trickster/v2/pkg/util/sets"
)

func TestNamesAndIDsAreUnique(t *testing.T) {
	usedIDs := sets.New([]types.ID{})
	usedNames := sets.New([]types.Name{})
	for _, m := range registry {
		if usedIDs.Contains(m.ID) {
			t.Errorf("mechanism %s reuses ID %d; IDs must be unique.",
				m.Name, m.ID)
		}
		if usedNames.Contains(m.Name) {
			t.Errorf("mechanism Name %s has been reused; Names must be unique.",
				m.Name)
		}
		if usedNames.Contains(m.ShortName) {
			t.Errorf("mechanism %s reuses ShortName %s; ShortNames must be unique.",
				m.Name, m.ShortName)
		}
		usedIDs.Set(m.ID)
		usedNames.Set(m.Name)
		usedNames.Set(m.ShortName)
	}
}

func TestIsRegistered(t *testing.T) {
	if ok := IsRegistered(rr.ShortName); !ok {
		t.Error("expected true")
	}
	if ok := IsRegistered(types.Name("invalid")); ok {
		t.Error("expected false")
	}
}
