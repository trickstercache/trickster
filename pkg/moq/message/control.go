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

package message

import (
	"bytes"

	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// Kind identifies a control message independently of the code a draft assigns it
type Kind uint8

// Control message kinds
const (
	KindSetup Kind = iota + 1
	KindGoAway
	KindRequestOK
	KindRequestError
	KindRequestUpdate
	KindSubscribe
	KindSubscribeOK
	KindPublish
	KindPublishOK
	KindPublishDone
	KindPublishStateNotify
	KindFetch
	KindFetchOK
	KindTrackStatus
	KindPublishNamespace
	KindSubscribeNamespace
	KindNamespace
	KindNamespaceDone
	KindSubscribeTracks
	KindPublishSkipped
)

var kindNames = [...]string{
	KindSetup: "SETUP", KindGoAway: "GOAWAY", KindRequestOK: "REQUEST_OK", KindRequestError: "REQUEST_ERROR",
	KindRequestUpdate: "REQUEST_UPDATE", KindSubscribe: "SUBSCRIBE", KindSubscribeOK: "SUBSCRIBE_OK",
	KindPublish: "PUBLISH", KindPublishOK: "PUBLISH_OK", KindPublishDone: "PUBLISH_DONE",
	KindPublishStateNotify: "PUBLISH_STATE_NOTIFY", KindFetch: "FETCH", KindFetchOK: "FETCH_OK",
	KindTrackStatus: "TRACK_STATUS", KindPublishNamespace: "PUBLISH_NAMESPACE",
	KindSubscribeNamespace: "SUBSCRIBE_NAMESPACE", KindNamespace: "NAMESPACE", KindNamespaceDone: "NAMESPACE_DONE",
	KindSubscribeTracks: "SUBSCRIBE_TRACKS", KindPublishSkipped: "PUBLISH_SKIPPED",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) && kindNames[k] != "" {
		return kindNames[k]
	}
	return "UNKNOWN"
}

// IsRequest reports whether a message of kind k opens a request stream and consumes a Request ID
func (k Kind) IsRequest() bool {
	switch k {
	case KindSubscribe, KindPublish, KindFetch, KindTrackStatus, KindPublishNamespace, KindSubscribeNamespace,
		KindSubscribeTracks:
		return true
	}
	return false
}

// Message is any control message
type Message interface {
	Kind() Kind
}

// FullTrackName names a track by its namespace and name
type FullTrackName struct {
	Namespace wire.Namespace
	Name      []byte
}

// Equal reports whether two names identify the same track
func (n FullTrackName) Equal(o FullTrackName) bool {
	return bytes.Equal(n.Name, o.Name) && n.Namespace.Equal(o.Namespace)
}

// Clone returns a deep copy, so the name outlives the buffer it was decoded from
func (n FullTrackName) Clone() FullTrackName {
	return FullTrackName{Namespace: n.Namespace.Clone(), Name: bytes.Clone(n.Name)}
}

// Setup is the first message on each control stream
type Setup struct {
	Path                  string
	HasPath               bool
	Authority             string
	HasAuthority          bool
	AuthTokens            []wire.Token
	MaxAuthTokenCacheSize uint64
	Implementation        string
	MaxFilterRanges       uint64
	MaxRequestUpdates     uint64
	// Unknown holds Setup Options this implementation does not interpret; they are ignored as the draft requires
	Unknown []wire.KV
}

// GoAway asks the peer to migrate to a new session, or, on a request stream, to re-issue that request
type GoAway struct {
	NewSessionURI string
	Timeout       uint64
	// RequestID names the request to migrate, carried only by draft -18
	RequestID    uint64
	HasRequestID bool
}

// RequestOK answers a request successfully; per-request names such as PUBLISH_NAMESPACE_OK are this message
type RequestOK struct {
	Params          Params
	TrackProperties Properties
}

// Redirect points a requester at another URI and, or, another track
type Redirect struct {
	ConnectURI string
	Track      FullTrackName
}

// RequestError rejects a request
type RequestError struct {
	Code          RequestCode
	RetryInterval uint64
	Reason        string
	Redirect      *Redirect
}

// RequestUpdate modifies an earlier request on the same stream
type RequestUpdate struct {
	RequestID uint64
	Params    Params
	// RequiredRequestID is the delta field draft -17 carries after the Request ID
	RequiredRequestID uint64
}

// Subscribe starts a subscription to a track
type Subscribe struct {
	RequestID         uint64
	Track             FullTrackName
	Params            Params
	RequiredRequestID uint64
}

// SubscribeOK accepts a subscription
type SubscribeOK struct {
	TrackAlias      uint64
	Params          Params
	TrackProperties Properties
}

// Publish starts a subscription from the publisher's side
type Publish struct {
	RequestID         uint64
	Track             FullTrackName
	TrackAlias        uint64
	Params            Params
	TrackProperties   Properties
	RequiredRequestID uint64
}

// PublishOK accepts a PUBLISH in draft -17; later drafts use REQUEST_OK
type PublishOK struct {
	Params Params
}

// PublishDone is the publisher's last message on a subscription
type PublishDone struct {
	Code        DoneCode
	StreamCount uint64
	Reason      string
}

// PublishStateNotify reports a change in a subscription's state that the subscriber did not request
type PublishStateNotify struct {
	Params Params
}

// Fetch requests a range of already published objects. Drafts before -20 carry the range or the joined
// subscription in the message rather than in LOCATION_FILTER; a codec maps them to and from Params.
type Fetch struct {
	RequestID         uint64
	Track             FullTrackName
	Params            Params
	Joining           *JoiningFetch
	RequiredRequestID uint64
}

// JoiningFetch is the joining form of FETCH used by drafts before -20
type JoiningFetch struct {
	RequestID uint64
	Start     uint64
	Relative  bool
}

// FetchOK accepts a fetch
type FetchOK struct {
	EndOfTrack      bool
	EndLocation     wire.Location
	Params          Params
	TrackProperties Properties
}

// TrackStatus asks for a track's current status without subscribing
type TrackStatus struct {
	RequestID         uint64
	Track             FullTrackName
	Params            Params
	RequiredRequestID uint64
}

// PublishNamespace advertises that the sender has tracks in namespaces matching Prefix
type PublishNamespace struct {
	RequestID         uint64
	Prefix            wire.Namespace
	Params            Params
	RequiredRequestID uint64
}

// SubscribeNamespace asks for the namespaces matching Prefix and changes to that set
type SubscribeNamespace struct {
	RequestID uint64
	Prefix    wire.Namespace
	Params    Params
	// Options is the subscribe options field draft -17 carries
	Options           uint64
	RequiredRequestID uint64
}

// Namespace reports a namespace matching a SUBSCRIBE_NAMESPACE, as the suffix after its prefix
type Namespace struct {
	Suffix wire.Namespace
}

// NamespaceDone withdraws a namespace reported by NAMESPACE
type NamespaceDone struct {
	Suffix wire.Namespace
}

// SubscribeTracks asks for a PUBLISH for every track in namespaces matching Prefix
type SubscribeTracks struct {
	RequestID uint64
	Prefix    wire.Namespace
	Params    Params
}

// PublishSkipped reports a track in a SUBSCRIBE_TRACKS that will not get a PUBLISH
type PublishSkipped struct {
	Suffix    wire.Namespace
	TrackName []byte
}

// Kind implementations
func (*Setup) Kind() Kind              { return KindSetup }
func (*GoAway) Kind() Kind             { return KindGoAway }
func (*RequestOK) Kind() Kind          { return KindRequestOK }
func (*RequestError) Kind() Kind       { return KindRequestError }
func (*RequestUpdate) Kind() Kind      { return KindRequestUpdate }
func (*Subscribe) Kind() Kind          { return KindSubscribe }
func (*SubscribeOK) Kind() Kind        { return KindSubscribeOK }
func (*Publish) Kind() Kind            { return KindPublish }
func (*PublishOK) Kind() Kind          { return KindPublishOK }
func (*PublishDone) Kind() Kind        { return KindPublishDone }
func (*PublishStateNotify) Kind() Kind { return KindPublishStateNotify }
func (*Fetch) Kind() Kind              { return KindFetch }
func (*FetchOK) Kind() Kind            { return KindFetchOK }
func (*TrackStatus) Kind() Kind        { return KindTrackStatus }
func (*PublishNamespace) Kind() Kind   { return KindPublishNamespace }
func (*SubscribeNamespace) Kind() Kind { return KindSubscribeNamespace }
func (*Namespace) Kind() Kind          { return KindNamespace }
func (*NamespaceDone) Kind() Kind      { return KindNamespaceDone }
func (*SubscribeTracks) Kind() Kind    { return KindSubscribeTracks }
func (*PublishSkipped) Kind() Kind     { return KindPublishSkipped }

// RequestIDOf returns the Request ID a request message carries, and false for any other message
func RequestIDOf(m Message) (uint64, bool) {
	switch v := m.(type) {
	case *Subscribe:
		return v.RequestID, true
	case *Publish:
		return v.RequestID, true
	case *Fetch:
		return v.RequestID, true
	case *TrackStatus:
		return v.RequestID, true
	case *PublishNamespace:
		return v.RequestID, true
	case *SubscribeNamespace:
		return v.RequestID, true
	case *SubscribeTracks:
		return v.RequestID, true
	case *RequestUpdate:
		return v.RequestID, true
	}
	return 0, false
}
