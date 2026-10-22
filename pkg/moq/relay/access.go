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

package relay

import (
	"slices"

	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/wire"
)

// Operation is what a request asks the relay to do with the tracks under a namespace
type Operation uint8

const (
	// OpSubscribe covers SUBSCRIBE, FETCH, TRACK_STATUS, SUBSCRIBE_NAMESPACE and SUBSCRIBE_TRACKS
	OpSubscribe Operation = iota
	// OpPublish covers PUBLISH and PUBLISH_NAMESPACE
	OpPublish
)

// Authorizer decides whether the holder of tokens may perform op on the tracks under namespace
type Authorizer interface {
	Authorize(op Operation, namespace wire.Namespace, tokens []wire.Token) bool
}

// Access is the publishing and authorization policy applied to each request; a reload replaces it
type Access struct {
	// Publish accepts PUBLISH and PUBLISH_NAMESPACE from downstream sessions
	Publish bool
	// Namespaces are the namespace prefixes publishers may claim; empty allows any
	Namespaces []wire.Namespace
	// MaxPublishersPerTrack bounds the publishers of one track at once; 0 allows any number
	MaxPublishersPerTrack int
	// Authorizer, when set, must allow every request, given its tokens and those of its session's SETUP
	Authorizer Authorizer
}

// SetAccess replaces the policy that new requests are judged by; requests already accepted keep running
func (r *Relay) SetAccess(a Access) {
	r.acc.Store(&a)
}

func (r *Relay) access() *Access {
	if a := r.acc.Load(); a != nil {
		return a
	}
	return &Access{}
}

func (a *Access) mayPublish(prefix wire.Namespace) bool {
	// a claim must lie within an allowed prefix, so it covers no namespace the policy withholds
	if !a.Publish {
		return false
	}
	if len(a.Namespaces) == 0 {
		return true
	}
	return slices.ContainsFunc(a.Namespaces, prefix.HasPrefix)
}

func (a *Access) authorized(req *session.Request, op Operation, ns wire.Namespace, tokens []wire.Token) bool {
	if a.Authorizer == nil {
		return true
	}
	if peer := req.Session().PeerSetup(); peer != nil && len(peer.AuthTokens) > 0 {
		tokens = append(slices.Clip(tokens), peer.AuthTokens...)
	}
	return a.Authorizer.Authorize(op, ns, tokens)
}
