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

package codec

import (
	"errors"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// SessionError returns a session error with code, or with PROTOCOL_VIOLATION when code is one draft -23 added and
// this draft lacks it
func (c *Codec) SessionError(code message.SessionCode, reason string) *message.SessionError {
	return &message.SessionError{Code: c.SessionCode(code), Reason: reason}
}

// SessionCode returns the code this draft closes a session with for code
func (c *Codec) SessionCode(code message.SessionCode) message.SessionCode {
	if code >= message.CodeInvalidStreamHeader && !c.Has(CapSpecificSessionCodes) {
		return message.CodeProtocolViolation
	}
	return code
}

// RequestCode returns the code this draft refuses a request with for code
func (c *Codec) RequestCode(code message.RequestCode) message.RequestCode {
	if code == message.RequestUnavailable && !c.Has(CapSpecificSessionCodes) {
		// a relay without stream credit is as loaded as one that refuses for load
		return message.RequestExcessiveLoad
	}
	return code
}

func (c *Codec) wireError(err error) error {
	// a framing error in a control message, unless it already carries a code
	return c.framingError(err, message.CodeMalformedMessage)
}

func (c *Codec) objectError(err error) error {
	// a framing error in an object or a stream header, unless it already carries a code
	return c.framingError(err, message.CodeMalformedObject)
}

func (c *Codec) framingError(err error, malformed message.SessionCode) error {
	if _, ok := errors.AsType[*message.SessionError](err); ok {
		return err
	}
	code := malformed
	switch {
	case errors.Is(err, wire.ErrTypeOverflow):
		code = message.CodeDeltaEncodingOverflow
	case errors.Is(err, wire.ErrTooLong), errors.Is(err, wire.ErrNamespaceTooLong):
		code = message.CodeFieldLengthExceeded
	case errors.Is(err, wire.ErrEmptyNamespaceField), errors.Is(err, wire.ErrNamespaceFields):
		code = message.CodeInvalidTrackNamespace
	}
	return c.SessionError(code, err.Error())
}
