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
package l4

// Observer receives one listener's relay events. The relay itself neither logs nor meters;
// an Observer is bound to its listener, so it can resolve whatever it counts with ahead of
// time and leave the per-datagram path a plain add.
type Observer interface {
	// Opened reports a connection or session admitted; Ended reports that it is over.
	Opened()
	Ended()
	// Result reports how a connection or session was disposed of: one of the Result consts.
	Result(result string)
	// Bytes reports payload relayed in a direction: DirectionIn or DirectionOut.
	Bytes(direction string, n int64)
	// Dropped reports a datagram dropped, by reason: one of the Drop consts.
	Dropped(reason string)
}

type nopObserver struct{}

func (nopObserver) Opened() {}

func (nopObserver) Ended() {}

func (nopObserver) Result(string) {}

func (nopObserver) Bytes(string, int64) {}

func (nopObserver) Dropped(string) {}
