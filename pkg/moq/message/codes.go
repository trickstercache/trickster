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

// Package message defines MoQ Transport control messages, parameters, properties and objects in a form
// shared by every supported draft; a codec translates them to and from one draft's wire format.
package message

import "fmt"

// SessionCode is a session termination error code
type SessionCode uint64

// Session termination codes; a codec translates those that differ by draft. 0x6 is named MALFORMED_KEY_VALUE from
// draft -23, and the codes from 0x200 are new in -23, which earlier drafts close with PROTOCOL_VIOLATION instead.
const (
	CodeNoError                  SessionCode = 0x0
	CodeInternalError            SessionCode = 0x1
	CodeUnauthorized             SessionCode = 0x2
	CodeProtocolViolation        SessionCode = 0x3
	CodeInvalidRequestID         SessionCode = 0x4
	CodeDuplicateTrackAlias      SessionCode = 0x5
	CodeKeyValueFormatting       SessionCode = 0x6
	CodeInvalidRequiredRequestID SessionCode = 0x7
	CodeInvalidPath              SessionCode = 0x8
	CodeMalformedPath            SessionCode = 0x9
	CodeGoAwayTimeout            SessionCode = 0x10
	CodeControlMessageTimeout    SessionCode = 0x11
	CodeDataStreamTimeout        SessionCode = 0x12
	CodeAuthTokenCacheOverflow   SessionCode = 0x13
	CodeDuplicateAuthTokenAlias  SessionCode = 0x14
	CodeVersionNegotiation       SessionCode = 0x15
	CodeMalformedAuthToken       SessionCode = 0x16
	CodeUnknownAuthTokenAlias    SessionCode = 0x17
	CodeExpiredAuthToken         SessionCode = 0x18
	CodeInvalidAuthority         SessionCode = 0x19
	CodeMalformedAuthority       SessionCode = 0x1a
	CodeTooManyRequestUpdates    SessionCode = 0x1b
	CodeInvalidStreamHeader      SessionCode = 0x200
	CodeMalformedObject          SessionCode = 0x201
	CodeInvalidObjectStatus      SessionCode = 0x202
	CodeTruncatedObject          SessionCode = 0x203
	CodeMalformedMessage         SessionCode = 0x300
	CodeInvalidField             SessionCode = 0x301
	CodeInvalidTrackNamespace    SessionCode = 0x302
	CodeUnexpectedMessage        SessionCode = 0x303
	CodeRoleViolation            SessionCode = 0x304
	CodeInvalidParameter         SessionCode = 0x305
	CodeClosedControlStream      SessionCode = 0x306
	CodeDeltaEncodingOverflow    SessionCode = 0x400
	CodeFieldLengthExceeded      SessionCode = 0x401
)

// RequestCode is a REQUEST_ERROR code
type RequestCode uint64

// REQUEST_ERROR codes; UNAVAILABLE is new in draft -23, and PREFIX_OVERLAP is gone from it
const (
	RequestInternalError         RequestCode = 0x0
	RequestUnauthorized          RequestCode = 0x1
	RequestTimeout               RequestCode = 0x2
	RequestNotSupported          RequestCode = 0x3
	RequestMalformedAuthToken    RequestCode = 0x4
	RequestExpiredAuthToken      RequestCode = 0x5
	RequestGoingAway             RequestCode = 0x6
	RequestExcessiveLoad         RequestCode = 0x9
	RequestUnavailable           RequestCode = 0xa
	RequestDoesNotExist          RequestCode = 0x10
	RequestInvalidRange          RequestCode = 0x11
	RequestMalformedTrack        RequestCode = 0x12
	RequestDuplicateSubscription RequestCode = 0x19
	RequestUninterested          RequestCode = 0x20
	RequestPrefixOverlap         RequestCode = 0x30
	RequestNamespaceTooLarge     RequestCode = 0x31
	RequestInvalidJoiningID      RequestCode = 0x32
	RequestUnsupportedExtension  RequestCode = 0x33
	RequestRedirect              RequestCode = 0x34
	RequestConflictingFilters    RequestCode = 0x35
	RequestInvalidFilter         RequestCode = 0x36
)

// DoneCode is a PUBLISH_DONE status code
type DoneCode uint64

// PUBLISH_DONE status codes as numbered from draft -18; a -17 codec swaps the wire values of the two that moved,
// and EXPIRED_AUTH_TOKEN is new in draft -23
const (
	DoneInternalError     DoneCode = 0x0
	DoneUnauthorized      DoneCode = 0x1
	DoneTrackEnded        DoneCode = 0x2
	DoneSubscriptionEnded DoneCode = 0x3
	DoneGoingAway         DoneCode = 0x4
	DoneTooFarBehind      DoneCode = 0x5
	DoneExpired           DoneCode = 0x6
	DoneExpiredAuthToken  DoneCode = 0x7
	DoneUpdateFailed      DoneCode = 0x8
	DoneExcessiveLoad     DoneCode = 0x9
	DoneMalformedTrack    DoneCode = 0x12
)

// ResetCode is a stream reset or STOP_SENDING code
type ResetCode uint64

// Stream reset codes as numbered from draft -18; a -17 codec translates the two that moved
const (
	ResetInternalError       ResetCode = 0x0
	ResetCancelled           ResetCode = 0x1
	ResetDeliveryTimeout     ResetCode = 0x2
	ResetSessionClosed       ResetCode = 0x3
	ResetGoingAway           ResetCode = 0x4
	ResetTooFarBehind        ResetCode = 0x5
	ResetUnknownObjectStatus ResetCode = 0x6
	ResetExpiredAuthToken    ResetCode = 0x7
	ResetExcessiveLoad       ResetCode = 0x9
	ResetMalformedTrack      ResetCode = 0x12
)

// SessionError is a failure that ends the whole session with Code
type SessionError struct {
	Code   SessionCode
	Reason string
}

func (e *SessionError) Error() string {
	return fmt.Sprintf("moq: session error %#x: %s", uint64(e.Code), e.Reason)
}

// Violation returns a PROTOCOL_VIOLATION session error
func Violation(reason string) *SessionError {
	return &SessionError{Code: CodeProtocolViolation, Reason: reason}
}

// RequestFailure is a problem that fails one request, answered with REQUEST_ERROR, rather than the session
type RequestFailure struct {
	Code   RequestCode
	Reason string
}

func (e *RequestFailure) Error() string {
	return fmt.Sprintf("moq: request error %#x: %s", uint64(e.Code), e.Reason)
}
