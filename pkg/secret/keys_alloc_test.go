//go:build !race

/*
 * Copyright 2026 The Trickster Authors
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

package secret

import "testing"

// the race detector makes a sync.Pool drop some of what it is given, so this runs without it
func TestSignerVerifyAllocations(t *testing.T) {
	s := signer(t, keyring(t, keyNew, keyOld), purposeA)
	tag := s.Sum(nil, msg)
	s.Verify(tag, msg)
	if n := testing.AllocsPerRun(100, func() { s.Verify(tag, msg) }); n != 0 {
		t.Errorf("Verify allocated %v times", n)
	}
}
