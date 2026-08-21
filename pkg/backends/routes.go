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

package backends

// RouteHealthStatus exposes the protocol-neutral health state used to admit a
// route target. It is intentionally smaller than healthcheck.Status so route
// selection does not depend on a particular health-check transport.
type RouteHealthStatus interface {
	Get() int32
}

// RouteTarget is a resolved backend and its optional health state. Protocol
// adapters decide how to execute the backend after the resolver selects it.
type RouteTarget struct {
	Backend Backend
	Status  RouteHealthStatus
}

// Available reports whether a target can receive new work.
func (t RouteTarget) Available() bool {
	return t.Backend != nil && (t.Status == nil || t.Status.Get() >= 0)
}

// RouteInput describes an authenticated identity without assuming an HTTP
// request or a particular native-protocol session type.
type RouteInput struct {
	RouterName    string
	Username      string
	Credential    string
	Authenticated bool
	// FallbackOnMappedUnavailable preserves HTTP User Router availability
	// semantics. Session protocols leave it false to prevent cross-target failover.
	FallbackOnMappedUnavailable bool
}

// RouteOutcome is a bounded route-selection result suitable for diagnostics
// and metrics. It never contains a username or other user-controlled value.
type RouteOutcome string

const (
	RouteOutcomeSelected    RouteOutcome = "selected"
	RouteOutcomeDefault     RouteOutcome = "default"
	RouteOutcomeNoRoute     RouteOutcome = "no_route"
	RouteOutcomeUnavailable RouteOutcome = "unavailable"
)

// RouteDecision is the protocol-neutral result of route selection. Protocol
// adapters apply credential replacement using their native representation.
type RouteDecision struct {
	Target             RouteTarget
	Outcome            RouteOutcome
	OutboundUsername   string
	OutboundCredential string
	ReplaceCredentials bool
}

// RouteResolver selects a runtime backend target for an authenticated identity.
// HTTP adapters resolve per request; session protocols resolve once after
// authentication and retain the target for the connection lifetime.
type RouteResolver interface {
	ResolveRoute(RouteInput) (RouteDecision, bool)
}
