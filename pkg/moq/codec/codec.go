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

// Package codec translates MoQ Transport messages and objects between the draft-neutral forms in the message
// package and the wire format of one negotiated draft. Behavior is selected by capability, never by draft number.
package codec

import (
	"slices"
	"strconv"

	"github.com/trickstercache/trickster/v2/pkg/moq/message"
)

// Caps are the wire features one draft has
type Caps uint32

// Capabilities that differ among the supported drafts
const (
	// CapRequiredRequestID carries a Required Request ID Delta after request IDs (draft -17)
	CapRequiredRequestID Caps = 1 << iota
	// CapSubscribeTracks has SUBSCRIBE_TRACKS, with SUBSCRIBE_NAMESPACE coded 0x50 (from -18)
	CapSubscribeTracks
	// CapRedirect lets REQUEST_ERROR carry a Redirect (from -18)
	CapRedirect
	// CapTrackPropertiesInRequestOK appends Track Properties to REQUEST_OK (from -18)
	CapTrackPropertiesInRequestOK
	// CapPublishOK answers PUBLISH with PUBLISH_OK rather than REQUEST_OK (draft -17)
	CapPublishOK
	// CapGoAwayRequestID lets GOAWAY carry a Request ID (draft -18)
	CapGoAwayRequestID
	// CapFirstObjectBit has the FIRST_OBJECT subgroup header bit (from -18)
	CapFirstObjectBit
	// CapPadding has padding streams and datagrams (from -18)
	CapPadding
	// CapSevenByteVarint may encode the 7-byte varint form (from -18)
	CapSevenByteVarint
	// CapRangeFilters has the Range Filter parameters and MAX_FILTER_RANGES (from -19)
	CapRangeFilters
	// CapJoiningFetch carries the FETCH range and joining form in the message (through -19)
	CapJoiningFetch
	// CapFillFetch has FILL_PARAMETERS and fill fetch streams (from -20)
	CapFillFetch
	// CapTypeFlagBitfields treats header and datagram types as flag bitfields (from -20)
	CapTypeFlagBitfields
	// CapPublishStateNotify has PUBLISH_STATE_NOTIFY (from -20)
	CapPublishStateNotify
	// CapLengthInferredLocationFilter infers LOCATION_FILTER fields from its length (-20 and -21)
	CapLengthInferredLocationFilter
	// CapTypedLocationFilter carries an explicit LOCATION_FILTER type (from -22)
	CapTypedLocationFilter
	// CapEndOfTimedOutRange has the End of Timed-Out Range fetch marker (from -20)
	CapEndOfTimedOutRange
)

// Codec encodes and decodes one draft's wire format; it holds no per-session state and is safe to share
type Codec struct {
	draft int
	alpn  string
	caps  Caps
}

const alpnPrefix = "moqt-"

// ALPNFor returns the ALPN that identifies a draft
func ALPNFor(draft int) string {
	return alpnPrefix + strconv.Itoa(draft)
}

func newCodec(draft int, caps Caps) *Codec {
	return &Codec{draft: draft, alpn: ALPNFor(draft), caps: caps}
}

const capsFrom20 = CapSubscribeTracks | CapRedirect | CapTrackPropertiesInRequestOK | CapFirstObjectBit |
	CapPadding | CapSevenByteVarint | CapRangeFilters | CapFillFetch | CapTypeFlagBitfields |
	CapPublishStateNotify | CapEndOfTimedOutRange

var codecs = []*Codec{
	newCodec(22, capsFrom20|CapTypedLocationFilter),
	newCodec(21, capsFrom20|CapLengthInferredLocationFilter),
	newCodec(20, capsFrom20|CapLengthInferredLocationFilter),
}

// Draft returns the draft number
func (c *Codec) Draft() int {
	return c.draft
}

// ALPN returns the draft's ALPN
func (c *Codec) ALPN() string {
	return c.alpn
}

// Has reports whether the draft has every capability in caps
func (c *Codec) Has(caps Caps) bool {
	return c.caps&caps == caps
}

// Supported returns every supported codec, newest draft first
func Supported() []*Codec {
	return slices.Clone(codecs)
}

// ForDraft returns the codec for a draft number
func ForDraft(draft int) (*Codec, bool) {
	for _, c := range codecs {
		if c.draft == draft {
			return c, true
		}
	}
	return nil, false
}

// ForALPN returns the codec for a negotiated ALPN
func ForALPN(alpn string) (*Codec, bool) {
	for _, c := range codecs {
		if c.alpn == alpn {
			return c, true
		}
	}
	return nil, false
}

// Drafts returns every supported draft number, newest first
func Drafts() []int {
	out := make([]int, len(codecs))
	for i, c := range codecs {
		out[i] = c.draft
	}
	return out
}

// ServerPreference orders the drafts a server picks from. -21 precedes -22: they differ only in LOCATION_FILTER's
// spelling, and a widely deployed implementation encodes -22 as -21, so a client offering both is served -21.
var ServerPreference = []int{21, 22, 20}

// ClientOffer is the default list a client offers; it omits -22, which a server preferring the newest draft
// would pick, for the interoperability reason given on ServerPreference
var ClientOffer = []int{21, 20}

// ALPNs returns the ALPNs of the given drafts that are supported, in the order given
func ALPNs(drafts []int) []string {
	out := make([]string, 0, len(drafts))
	for _, d := range drafts {
		if c, ok := ForDraft(d); ok {
			out = append(out, c.alpn)
		}
	}
	return out
}

var messageTypes = map[message.Kind]uint64{
	message.KindSetup:              0x2f00,
	message.KindGoAway:             0x10,
	message.KindRequestOK:          0x7,
	message.KindRequestError:       0x5,
	message.KindRequestUpdate:      0x2,
	message.KindSubscribe:          0x3,
	message.KindSubscribeOK:        0x4,
	message.KindPublish:            0x1d,
	message.KindPublishDone:        0xb,
	message.KindPublishStateNotify: 0x22,
	message.KindFetch:              0x16,
	message.KindFetchOK:            0x18,
	message.KindTrackStatus:        0xd,
	message.KindPublishNamespace:   0x6,
	message.KindSubscribeNamespace: 0x50,
	message.KindNamespace:          0x8,
	message.KindNamespaceDone:      0xe,
	message.KindSubscribeTracks:    0x51,
	message.KindPublishSkipped:     0xf,
}

var messageKinds = func() map[uint64]message.Kind {
	out := make(map[uint64]message.Kind, len(messageTypes))
	for k, t := range messageTypes {
		out[t] = k
	}
	return out
}()

// MessageType returns the wire code of a control message kind, and false when the draft has no such message
func (c *Codec) MessageType(k message.Kind) (uint64, bool) {
	switch k {
	case message.KindPublishStateNotify:
		if !c.Has(CapPublishStateNotify) {
			return 0, false
		}
	case message.KindSubscribeTracks:
		if !c.Has(CapSubscribeTracks) {
			return 0, false
		}
	case message.KindPublishOK:
		return 0, false
	}
	t, ok := messageTypes[k]
	return t, ok
}

// MessageKind returns the kind of a control message wire code, and false when the draft has none
func (c *Codec) MessageKind(t uint64) (message.Kind, bool) {
	k, ok := messageKinds[t]
	if !ok {
		return 0, false
	}
	if _, ok = c.MessageType(k); !ok {
		return 0, false
	}
	return k, true
}
