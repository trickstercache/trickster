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

package status

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupStatusString(t *testing.T) {
	cases := []struct {
		lookup LookupStatus
		want   string
	}{
		{LookupStatusHit, "hit"},
		{LookupStatusKeyMiss, "kmiss"},
		{LookupStatus(99), "99"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, c.lookup.String())
	}
}

func TestLookupStatus_cacheLookupStatusValues(t *testing.T) {
	// test that the internal slice mapping is correct for all statuses
	for status := range MaxLookupStatus() {
		require.Equal(t, status, cacheLookupStatusValues[status].LookupStatus)
	}
}
