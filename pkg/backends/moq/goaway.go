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

package moq

import (
	"sync"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
)

const (
	reasonGoingAway = "going away"
	retryNewSession = 1 // a Retry Interval asking for a retry at once (§9.4.2), on the new session
)

type departure struct {
	mu      sync.Mutex
	leaving bool
	uri     string
}

func (d *departure) leave(uri string) {
	d.mu.Lock()
	d.leaving, d.uri = true, uri
	d.mu.Unlock()
}

func (d *departure) refuses(req *session.Request) bool {
	// a session that was sent GOAWAY takes no new requests (§9.2): each is redirected to the session's new URI where
	// the draft has REDIRECT, else refused with GOING_AWAY
	d.mu.Lock()
	leaving, uri := d.leaving, d.uri
	d.mu.Unlock()
	if !leaving {
		return false
	}
	if uri != "" && req.Session().Codec().Has(codec.CapRedirect) {
		if rd := redirectFor(req.Message(), uri); rd != nil {
			if req.Send(&message.RequestError{
				Code: message.RequestRedirect, Reason: reasonGoingAway,
				Redirect: rd,
			}) == nil {
				_ = req.CloseSend()
			}
			return true
		}
	}
	_ = req.Reject(message.RequestGoingAway, reasonGoingAway, retryNewSession)
	return true
}

func redirectFor(m message.Message, uri string) *message.Redirect {
	// the same track is asked for at the new URI; a namespace-scoped request keeps its namespace and names no track
	switch v := m.(type) {
	case *message.Subscribe:
		return &message.Redirect{ConnectURI: uri, Track: v.Track}
	case *message.TrackStatus:
		return &message.Redirect{ConnectURI: uri, Track: v.Track}
	case *message.Publish:
		return &message.Redirect{ConnectURI: uri, Track: v.Track}
	case *message.Fetch:
		if v.Joining != nil {
			// a joining fetch names a subscription of this session, which a new session cannot join
			return nil
		}
		return &message.Redirect{ConnectURI: uri, Track: v.Track}
	case *message.PublishNamespace:
		return &message.Redirect{ConnectURI: uri, Track: message.FullTrackName{Namespace: v.Prefix}}
	case *message.SubscribeNamespace:
		return &message.Redirect{ConnectURI: uri, Track: message.FullTrackName{Namespace: v.Prefix}}
	case *message.SubscribeTracks:
		return &message.Redirect{ConnectURI: uri, Track: message.FullTrackName{Namespace: v.Prefix}}
	}
	return nil
}

type leavable struct {
	session.Handler
	departure
}

func (l *leavable) HandleRequest(req *session.Request) {
	if !l.refuses(req) {
		l.Handler.HandleRequest(req)
	}
}
