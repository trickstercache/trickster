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

import "strconv"

// Type is a DNS resource record type
type Type uint16

// Resource record types recognized by this client. Records of any other
// type unpack as *Unknown rather than failing the message.
const (
	TypeA    Type = 1
	TypeAAAA Type = 28
	TypeSRV  Type = 33
	TypeOPT  Type = 41
)

// Class is a DNS resource record class
type Class uint16

// ClassINET is the Internet record class, the only class this client uses
const ClassINET Class = 1

// RCode is the response code returned by a DNS server
type RCode uint16

// Response codes defined by RFC 1035
const (
	RCodeSuccess        RCode = 0
	RCodeFormatError    RCode = 1
	RCodeServerFailure  RCode = 2
	RCodeNameError      RCode = 3
	RCodeNotImplemented RCode = 4
	RCodeRefused        RCode = 5
)

var rcodeNames = map[RCode]string{
	RCodeSuccess:        "NOERROR",
	RCodeFormatError:    "FORMERR",
	RCodeServerFailure:  "SERVFAIL",
	RCodeNameError:      "NXDOMAIN",
	RCodeNotImplemented: "NOTIMP",
	RCodeRefused:        "REFUSED",
}

// String returns the response code's mnemonic, or RCODE<n> when unknown
func (r RCode) String() string {
	if s, ok := rcodeNames[r]; ok {
		return s
	}
	return "RCODE" + strconv.Itoa(int(r))
}
