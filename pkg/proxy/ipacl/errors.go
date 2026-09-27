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

package ipacl

import "errors"

// Sentinel errors from Compile. Callers wrap them with the entry or file
// that failed; tests and the loader match them with errors.Is.
var (
	// ErrInvalidEntry is an address, CIDR or all token that does not parse.
	ErrInvalidEntry = errors.New("invalid ip acl entry")
	// ErrInvalidMatch is a match mode other than longest or ordered, or a
	// list shape that mode does not allow.
	ErrInvalidMatch = errors.New("invalid ip acl match")
	// ErrInvalidDefault is a default other than allow or deny.
	ErrInvalidDefault = errors.New("invalid ip acl default")
	// ErrInvalidSource is a source other than client_ip or peer.
	ErrInvalidSource = errors.New("invalid ip acl source")
	// ErrInvalidAction is an action other than reject or drop.
	ErrInvalidAction = errors.New("invalid ip acl action")
	// ErrInvalidStatus is an HTTP status outside 400-599.
	ErrInvalidStatus = errors.New("invalid ip acl status")
	// ErrInvalidRule is an ordered rule that does not carry exactly one action.
	ErrInvalidRule = errors.New("invalid ip acl rule")
	// ErrInvalidFile is a list file that is missing, unreadable or not a file.
	ErrInvalidFile = errors.New("invalid ip acl file")
	// ErrInvalidName is an empty ACL name or the reserved reference none.
	ErrInvalidName = errors.New("invalid ip acl name")
)
