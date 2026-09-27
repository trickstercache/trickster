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
package sticky

import (
	"testing"
	"time"
)

// the race detector makes a sync.Pool drop some of what it is given, so this runs without it
func TestTokenReadDoesNotAllocate(t *testing.T) {
	c := codec(t, albName, time.Hour, 0)
	tok := c.Mint(Token{Path: nested, Born: born, Issued: issued})
	c.Read(tok, issued)
	if n := testing.AllocsPerRun(100, func() { c.Read(tok, issued) }); n != 0 {
		t.Errorf("Read allocated %v times", n)
	}
	// a mint allocates only the string it returns
	if n := testing.AllocsPerRun(100, func() { c.Mint(Token{Path: nested, Born: born, Issued: issued}) }); n != 1 {
		t.Errorf("Mint allocated %v times", n)
	}
}
