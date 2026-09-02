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

package client

import "errors"

var (
	// ErrShortMessage is returned when a message ends before a field it
	// claims to contain
	ErrShortMessage = errors.New("dns message is shorter than its contents")
	// ErrLongMessage is returned when a message or one of its sections
	// exceeds the size the wire format can express
	ErrLongMessage = errors.New("dns message exceeds the maximum encodable size")
	// ErrInvalidName is returned for a domain name that cannot be encoded
	// or that is malformed on the wire
	ErrInvalidName = errors.New("invalid dns domain name")
	// ErrNameLoop is returned when compression pointers in a message form
	// a cycle
	ErrNameLoop = errors.New("dns name compression pointer loop")
	// ErrInvalidRecord is returned when a record's data does not match the
	// layout its type requires
	ErrInvalidRecord = errors.New("invalid dns resource record data")
	// ErrNoResponse is returned when a server's reply does not answer the
	// question that was asked
	ErrNoResponse = errors.New("dns response does not match the query")
	// ErrNoServers is returned when a resolver configuration file names no
	// usable nameservers
	ErrNoServers = errors.New("no dns nameservers configured")
)
