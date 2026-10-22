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

package session

import (
	"errors"
	"testing"

	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

func TestWebTransportCodes(t *testing.T) {
	// the forward mapping of draft-ietf-webtrans-http3 section 4.4, which streamCode inverts
	toHTTP := func(n uint64) uint64 { return wtFirstCode + n + n/0x1e }
	for _, n := range []uint64{0, 1, 0x1d, 0x1e, 0x1f, 0x3c, 1 << 20, 1<<32 - 1} {
		if got := streamCode(toHTTP(n)); got != n {
			t.Errorf("code %#x decoded as %#x", n, got)
		}
	}
	reserved := uint64(wtFirstCode + 0x1e)
	for (reserved-wtReserved)%wtSkip != 0 {
		reserved++
	}
	for _, c := range []uint64{0, 7, wtFirstCode - 1, wtLastCode + 1, reserved} {
		if got := streamCode(c); got != c {
			t.Errorf("a code outside the WebTransport range is kept: %#x became %#x", c, got)
		}
	}
	if wtCode(5, 1) != 5 || wtCode(1<<40, 1) != 1 {
		t.Error("codes past 32 bits fall back")
	}
}

func TestWebTransportErrors(t *testing.T) {
	if wtErr(nil) != nil {
		t.Error("no error")
	}
	if se, ok := errors.AsType[*StreamError](wtErr(&webtransport.StreamError{ErrorCode: 3, Remote: true})); !ok ||
		se.Code != 3 || !se.Remote {
		t.Error("stream error")
	}
	ce, ok := errors.AsType[*CloseError](wtErr(&webtransport.SessionError{ErrorCode: 2, Message: "m", Remote: true}))
	if !ok || ce.Code != 2 || ce.Reason != "m" || !ce.Remote {
		t.Error("session error")
	}
	ce, ok = errors.AsType[*CloseError](wtErr(&http3.Error{ErrorCode: http3.ErrCodeDatagramError, ErrorMessage: "x"}))
	if !ok || ce.Code != uint64(http3.ErrCodeDatagramError) || ce.Reason != "x" || ce.Remote {
		t.Error("an HTTP/3 error ending the session")
	}
	other := errors.New("other")
	if !errors.Is(wtErr(other), other) {
		t.Error("anything else passes through")
	}
}
